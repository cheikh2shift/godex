# Vision WASM Runtime

This directory contains the Rust/WASI image analysis module used by GoDex for local image analysis.

## Build (local)

```bash
rustup target add wasm32-wasip1
cargo build --release --target wasm32-wasip1
cp target/wasm32-wasip1/release/vision.wasm ./vision.wasm
```

## Notes
- The workflow `.github/workflows/build-vision-wasm.yml` builds `vision.wasm` and commits it into this directory.
- The Go app downloads `vision.wasm`, the MobileNetV2 ONNX model, and labels into `~/.godex/models/vision/` on startup.
