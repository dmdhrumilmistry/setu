//! The webrtc-rs side of the spike: same command line and behaviour as
//! gopeer (`--role offer|answer --send N --recv N`, SDP as one JSON line on
//! stdin/stdout, a reliable ordered "setu" channel, 32 KiB messages,
//! pause above 1 MiB queued and resume at 256 KiB).

use std::io::{BufRead, Write};
use std::sync::Arc;
use std::time::Instant;

use bytes::BytesMut;
use futures::FutureExt;
use webrtc::data_channel::{DataChannel, DataChannelEvent};
use webrtc::peer_connection::{
    MediaEngine, PeerConnection, PeerConnectionBuilder, PeerConnectionEventHandler, RTCConfigurationBuilder,
    RTCIceGatheringState, RTCSessionDescription, Registry, register_default_interceptors,
};
use webrtc::runtime::{Runtime, Sender, channel, default_runtime};

struct Args {
    role: String,
    send: usize,
    recv: usize,
}

fn args() -> Args {
    let mut a = Args { role: "offer".into(), send: 0, recv: 0 };
    let v: Vec<String> = std::env::args().collect();
    let mut i = 1;
    while i + 1 < v.len() {
        match v[i].trim_start_matches('-') {
            "role" => a.role = v[i + 1].clone(),
            "send" => a.send = v[i + 1].parse().unwrap(),
            "recv" => a.recv = v[i + 1].parse().unwrap(),
            _ => {}
        }
        i += 2;
    }
    a
}

fn fingerprint(sdp: &str) -> String {
    sdp.lines()
        .find_map(|l| l.strip_prefix("a=fingerprint:sha-256 "))
        .map(|f| f.trim().replace(':', "").to_lowercase())
        .unwrap_or_else(|| "?".into())
}

macro_rules! log { ($role:expr, $($t:tt)*) => { eprintln!("[webrtc-rs/{}] {}", $role, format!($($t)*)) } }

#[derive(Clone)]
struct Handler {
    rt: Arc<dyn Runtime>,
    gathered: Sender<()>,
    channel: Sender<Arc<dyn DataChannel>>,
}

#[async_trait::async_trait]
impl PeerConnectionEventHandler for Handler {
    async fn on_ice_gathering_state_change(&self, s: RTCIceGatheringState) {
        if s == RTCIceGatheringState::Complete {
            let _ = self.gathered.try_send(());
        }
    }
    async fn on_data_channel(&self, dc: Arc<dyn DataChannel>) {
        let _ = self.channel.try_send(dc);
        let _ = &self.rt;
    }
}

fn main() -> anyhow::Result<()> {
    let rt = default_runtime().expect("tokio runtime");
    let mut out = None;
    {
        let slot = &mut out;
        let rt2 = rt.clone();
        rt.block_on(Box::pin(async move { *slot = Some(run(rt2).await) }));
    }
    out.unwrap()
}

async fn run(rt: Arc<dyn Runtime>) -> anyhow::Result<()> {
    let a = args();
    let role = a.role.clone();
    let (gtx, mut grx) = channel::<()>(1);
    let (ctx, mut crx) = channel::<Arc<dyn DataChannel>>(1);
    let mut media = MediaEngine::default();
    let registry = register_default_interceptors(Registry::new(), &mut media)?;
    let pc = PeerConnectionBuilder::new()
        .with_configuration(RTCConfigurationBuilder::new().build())
        .with_media_engine(media)
        .with_interceptor_registry(registry)
        .with_handler(Arc::new(Handler { rt: rt.clone(), gathered: gtx, channel: ctx }))
        .with_runtime(rt.clone())
        .with_udp_addrs(vec!["0.0.0.0:0".to_string()])
        .build()
        .await?;

    let stdin = std::io::stdin();
    let mut line = String::new();
    let dc: Arc<dyn DataChannel>;
    if role == "offer" {
        dc = pc.create_data_channel("setu", None).await?;
        let o = pc.create_offer(None).await?;
        pc.set_local_description(o).await?;
        grx.recv().await;
        println!("{}", serde_json::to_string(&pc.local_description().await.unwrap())?);
        std::io::stdout().flush()?;
        stdin.lock().read_line(&mut line)?;
        let ans: RTCSessionDescription = serde_json::from_str(&line)?;
        pc.set_remote_description(ans).await?;
    } else {
        stdin.lock().read_line(&mut line)?;
        let offer: RTCSessionDescription = serde_json::from_str(&line)?;
        pc.set_remote_description(offer).await?;
        let ans = pc.create_answer(None).await?;
        pc.set_local_description(ans).await?;
        grx.recv().await;
        println!("{}", serde_json::to_string(&pc.local_description().await.unwrap())?);
        std::io::stdout().flush()?;
        dc = crx.recv().await.expect("data channel");
    }
    dc.set_buffered_amount_low_threshold(256 * 1024).await?;
    dc.set_buffered_amount_high_threshold(1024 * 1024).await?;

    let chunk = BytesMut::from(vec![7u8; 32 * 1024].as_slice());
    let (mut open, mut paused) = (role != "offer", false); // an accepted channel is already open
    let (mut sent, mut got) = (0usize, 0usize);
    let (mut send_start, mut recv_start): (Option<Instant>, Option<Instant>) = (None, None);
    let mut reported = false;
    let report_open = |pc_local: String, pc_remote: String| {
        log!(role, "channel \"setu\" open; local fp {} remote fp {}", fingerprint(&pc_local), fingerprint(&pc_remote));
    };
    if open {
        report_open(pc.local_description().await.unwrap().sdp, pc.remote_description().await.unwrap().sdp);
    }
    loop {
        if open && !paused && a.send > sent {
            if send_start.is_none() {
                send_start = Some(Instant::now());
            }
            futures::select! {
                ev = dc.poll().fuse() => match ev {
                    Some(DataChannelEvent::OnBufferedAmountHigh) => paused = true,
                    Some(DataChannelEvent::OnBufferedAmountLow) => paused = false,
                    Some(DataChannelEvent::OnMessage(m)) => { recv_start.get_or_insert_with(Instant::now); got += m.data.len(); }
                    Some(DataChannelEvent::OnClose) | None => break,
                    _ => {}
                },
                r = dc.send(chunk.clone()).fuse() => {
                    if r.is_ok() { sent += chunk.len(); }
                    if sent >= a.send && !reported {
                        log!(role, "queued {} bytes in {:?}", sent, send_start.unwrap().elapsed());
                        reported = true;
                    }
                }
            }
        } else {
            match dc.poll().await {
                Some(DataChannelEvent::OnOpen) => {
                    open = true;
                    report_open(pc.local_description().await.unwrap().sdp, pc.remote_description().await.unwrap().sdp);
                }
                Some(DataChannelEvent::OnBufferedAmountHigh) => paused = true,
                Some(DataChannelEvent::OnBufferedAmountLow) => paused = false,
                Some(DataChannelEvent::OnMessage(m)) => {
                    recv_start.get_or_insert_with(Instant::now);
                    got += m.data.len();
                }
                Some(DataChannelEvent::OnClose) | None => break,
                _ => {}
            }
        }
        if a.recv > 0 && got >= a.recv {
            let d = recv_start.unwrap().elapsed();
            log!(role, "RESULT received {} bytes in {:?} = {:.1} MiB/s", got, d, got as f64 / 1048576.0 / d.as_secs_f64());
            break;
        }
    }
    if a.recv == 0 {
        futures::future::pending::<()>().await;
    }
    pc.close().await?;
    Ok(())
}
