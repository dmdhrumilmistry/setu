# Rust migration spikes (Phase 0)

Throwaway measurements behind the Phase 0 results in
[docs/rust-migration.md](../../docs/rust-migration.md). Not part of the
setu build; nothing here ships.

## WebRTC: Rust stacks against pion

`webrtc/gopeer` is a pion peer with setu's settings (a reliable, ordered
`setu` data channel, 32 KiB messages, pause above 1 MiB queued, resume at
256 KiB). `webrtc/src/bin/{webrtcrs,str0m}.rs` do the same with
webrtc-rs and str0m. `pair.py` relays the SDP between two peers and reports
throughput, both DTLS fingerprints and the sender's CPU time.

```sh
(cd webrtc/gopeer && go build -o ../gopeer.bin .)
(cd webrtc && cargo build --release)
cd webrtc
N=268435456; IP=$(hostname -I | cut -d' ' -f1)
./pair.py "./gopeer.bin -role offer -send $N" "./gopeer.bin -role answer -recv $N"        # pion -> pion
./pair.py "target/release/webrtcrs --role offer --send $N" "./gopeer.bin -role answer -recv $N"
./pair.py "target/release/str0m --ip $IP --role offer --send $N" "./gopeer.bin -role answer -recv $N"
```

`str0m-only` is the str0m peer in a crate of its own, to check what it needs
for static and cross builds (`cargo zigbuild --release --target
x86_64-unknown-linux-musl`, `aarch64-unknown-linux-musl`,
`x86_64-pc-windows-gnu`; `pip install cargo-zigbuild` provides Zig).

## Image encoding

`encode/genframes` writes the desktop benchmark's two 1080p frames (same
generator and seeds as `internal/desktop/bench_test.go`) as raw RGBA and
times Go's encoders on them; `encode` times Rust encoders on the same
pixels with the same settings.

```sh
mkdir -p /tmp/frames
(cd encode/genframes && go run . /tmp/frames)
(cd encode && cargo run --release -- /tmp/frames)
```
