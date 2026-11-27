//! The str0m side of the spike: same command line and behaviour as gopeer
//! (`--role offer|answer --send N --recv N --ip ADDR`). str0m is sans-IO,
//! so this owns the UDP socket and the event loop, as a real setu host
//! would.

use std::io::{BufRead, ErrorKind, Write};
use std::net::{SocketAddr, UdpSocket};
use std::time::{Duration, Instant};

use str0m::change::{SdpAnswer, SdpOffer};
use str0m::channel::ChannelId;
use str0m::net::{Protocol, Receive};
use str0m::{Candidate, Event, Input, Output, Rtc};

const CHUNK: usize = 32 * 1024;
const HIGH: usize = 1024 * 1024;
const LOW: usize = 256 * 1024;

fn fingerprint(sdp: &str) -> String {
    sdp.lines()
        .find_map(|l| l.strip_prefix("a=fingerprint:sha-256 "))
        .map(|f| f.trim().replace(':', "").to_lowercase())
        .unwrap_or_else(|| "?".into())
}

fn main() -> anyhow::Result<()> {
    let v: Vec<String> = std::env::args().collect();
    let get = |k: &str| v.iter().position(|a| a.trim_start_matches('-') == k).map(|i| v[i + 1].clone());
    let role = get("role").unwrap_or_else(|| "offer".into());
    let send: usize = get("send").map(|s| s.parse().unwrap()).unwrap_or(0);
    let recv: usize = get("recv").map(|s| s.parse().unwrap()).unwrap_or(0);
    let ip = get("ip").expect("--ip <a non-loopback local address>");
    let log = |m: String| eprintln!("[str0m/{role}] {m}");

    let socket = UdpSocket::bind(format!("{ip}:0"))?;
    let local: SocketAddr = socket.local_addr()?;
    let mut rtc = Rtc::builder().build(Instant::now());
    rtc.add_local_candidate(Candidate::host(local, "udp")?);

    let stdin = std::io::stdin();
    let mut line = String::new();
    let (local_sdp, remote_sdp);
    let mut cid: Option<ChannelId> = None;
    if role == "offer" {
        let mut api = rtc.sdp_api();
        cid = Some(api.add_channel("setu".into()));
        let (offer, pending) = api.apply().expect("offer");
        local_sdp = offer.to_sdp_string();
        println!("{}", serde_json::to_string(&offer)?);
        std::io::stdout().flush()?;
        stdin.lock().read_line(&mut line)?;
        let answer: SdpAnswer = serde_json::from_str(&line)?;
        remote_sdp = answer.to_sdp_string();
        rtc.sdp_api().accept_answer(pending, answer)?;
    } else {
        stdin.lock().read_line(&mut line)?;
        let offer: SdpOffer = serde_json::from_str(&line)?;
        remote_sdp = offer.to_sdp_string();
        let answer = rtc.sdp_api().accept_offer(offer)?;
        local_sdp = answer.to_sdp_string();
        println!("{}", serde_json::to_string(&answer)?);
        std::io::stdout().flush()?;
    }

    let chunk = vec![7u8; CHUNK];
    let mut buf = vec![0u8; 2000];
    let (mut open, mut sent, mut got) = (false, 0usize, 0usize);
    let (mut send_start, mut recv_start): (Option<Instant>, Option<Instant>) = (None, None);
    let mut reported = false;
    loop {
        // Feed the channel while it has room (the same flow control as setu).
        if let (true, Some(id)) = (open, cid) {
            if let Some(mut ch) = rtc.channel(id) {
                while sent < send && ch.buffered_amount() < HIGH {
                    match ch.write(true, &chunk) {
                        Ok(true) => sent += CHUNK,
                        _ => break,
                    }
                }
            }
            if sent >= send && send > 0 && !reported {
                log(format!("queued {sent} bytes in {:?}", send_start.unwrap().elapsed()));
                reported = true;
            }
        }
        // Drain outputs until str0m wants to wait.
        let deadline = loop {
            match rtc.poll_output()? {
                Output::Transmit(t) => {
                    socket.send_to(&t.contents, t.destination)?;
                }
                Output::Timeout(t) => break t,
                Output::Event(e) => match e {
                    Event::ChannelOpen(id, label) => {
                        cid = Some(id);
                        open = true;
                        send_start = Some(Instant::now());
                        if let Some(mut ch) = rtc.channel(id) {
                            ch.set_buffered_amount_low_threshold(LOW);
                        }
                        log(format!("channel {label:?} open; local fp {} remote fp {}", fingerprint(&local_sdp), fingerprint(&remote_sdp)));
                    }
                    Event::ChannelData(d) => {
                        recv_start.get_or_insert_with(Instant::now);
                        got += d.data.len();
                    }
                    Event::ChannelBufferedAmountLow(_) => {}
                    _ => {}
                },
            }
        };
        if recv > 0 && got >= recv {
            let d = recv_start.unwrap().elapsed();
            log(format!("RESULT received {got} bytes in {d:?} = {:.1} MiB/s", got as f64 / 1048576.0 / d.as_secs_f64()));
            return Ok(());
        }
        let wait = deadline.saturating_duration_since(Instant::now()).max(Duration::from_micros(1));
        socket.set_read_timeout(Some(wait))?;
        buf.resize(2000, 0);
        match socket.recv_from(&mut buf) {
            Ok((n, source)) => {
                if let Ok(contents) = buf[..n].try_into() {
                    rtc.handle_input(Input::Receive(Instant::now(), Receive { proto: Protocol::Udp, source, destination: local, contents }))?;
                }
            }
            Err(e) if matches!(e.kind(), ErrorKind::WouldBlock | ErrorKind::TimedOut) => {
                rtc.handle_input(Input::Timeout(Instant::now()))?;
            }
            Err(e) => return Err(e.into()),
        }
    }
}
