//! Times Rust image encoders on the frames genframes wrote, with the
//! same settings as setu's Go encoder: JPEG quality 70 with 4:2:0 chroma
//! subsampling (Go's default), and for few-colour frames a palette
//! conversion plus PNG with fast deflate.

use std::collections::HashMap;
use std::time::Instant;

const W: usize = 1920;
const H: usize = 1080;
const N: u32 = 20;

fn time<F: FnMut() -> usize>(label: &str, mut f: F) {
    let size = f(); // warm up
    let start = Instant::now();
    for _ in 0..N {
        f();
    }
    let ms = start.elapsed().as_secs_f64() * 1000.0 / N as f64;
    println!("{label:<34} {ms:6.1} ms/frame {size:7} bytes");
}

/// setu's rule: a palette if the frame has at most 256 colours.
fn paletted(rgba: &[u8]) -> Option<(Vec<u8>, Vec<u8>, Vec<u8>)> {
    let mut index: HashMap<u32, u8> = HashMap::with_capacity(64);
    let (mut pal, mut trns) = (Vec::new(), Vec::new());
    let mut out = vec![0u8; W * H];
    for (i, p) in rgba.chunks_exact(4).enumerate() {
        let k = u32::from_be_bytes([p[0], p[1], p[2], p[3]]);
        let n = index.len();
        let idx = match index.get(&k) {
            Some(&v) => v,
            None => {
                if n == 256 {
                    return None;
                }
                index.insert(k, n as u8);
                pal.extend_from_slice(&p[..3]);
                trns.push(p[3]);
                n as u8
            }
        };
        out[i] = idx;
    }
    Some((out, pal, trns))
}

fn png_indexed(rgba: &[u8], c: png::Compression, f: png::Filter) -> usize {
    let (pix, pal, trns) = paletted(rgba).expect("few colours");
    let mut buf = Vec::new();
    let mut e = png::Encoder::new(&mut buf, W as u32, H as u32);
    e.set_color(png::ColorType::Indexed);
    e.set_depth(png::BitDepth::Eight);
    e.set_palette(pal);
    e.set_trns(trns);
    e.set_compression(c);
    e.set_filter(f);
    let mut w = e.write_header().unwrap();
    w.write_image_data(&pix).unwrap();
    w.finish().unwrap();
    buf.len()
}

fn main() -> anyhow::Result<()> {
    let dir = std::env::args().nth(1).expect("frames directory");
    let text = std::fs::read(format!("{dir}/text.rgba"))?;
    let photo = std::fs::read(format!("{dir}/photo.rgba"))?;
    assert!(paletted(&photo).is_none(), "photo frame should take the JPEG path");

    time("png (fdeflate Fast), text", || png_indexed(&text, png::Compression::Fast, png::Filter::Adaptive));
    time("png (Fastest), text", || png_indexed(&text, png::Compression::Fastest, png::Filter::Adaptive));
    time("png (Balanced), text", || png_indexed(&text, png::Compression::Balanced, png::Filter::Adaptive));
    time("png (Fast, no filter), text", || png_indexed(&text, png::Compression::Fast, png::Filter::NoFilter));
    time("png (Balanced, no filter), text", || png_indexed(&text, png::Compression::Balanced, png::Filter::NoFilter));
    time("jpeg-encoder (AVX2), photo", || {
        let mut buf = Vec::new();
        jpeg_encoder::Encoder::new(&mut buf, 70).encode(&photo, W as u16, H as u16, jpeg_encoder::ColorType::Rgba).unwrap();
        buf.len()
    });
    time("turbojpeg (libjpeg-turbo), photo", || {
        let img = turbojpeg::Image { pixels: &photo[..], width: W, pitch: W * 4, height: H, format: turbojpeg::PixelFormat::RGBA };
        turbojpeg::compress(img, 70, turbojpeg::Subsamp::Sub2x2).unwrap().len()
    });
    Ok(())
}
