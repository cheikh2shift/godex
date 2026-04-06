# OCR WASM Runtime

This directory contains the Rust/WASI OCR module used by GoDex for text extraction from images.

## Build (local)

```bash
rustup target add wasm32-wasip1
cargo build --release --target wasm32-wasip1
cp target/wasm32-wasip1/release/ocr.wasm ./ocr.wasm
```

## Notes
- The workflow `.github/workflows/build-ocr-wasm.yml` builds `ocr.wasm` and commits it into this directory.
- The Go app downloads `ocr.wasm` into `~/.godex/models/ocr/` on startup.
