#!/bin/bash
~/.godex/bin/llama-b8665/llama-server \
  -m ~/.godex/models/vision/LightOnOCR-2-1B-Q4_K_M.gguf \
  --mmproj ~/.godex/models/vision/LightOnOCR-2-1B-mmproj-f16.gguf \
  --port 18888 --host 127.0.0.1 \
  -ngl 99 \
  -t 16 -tb 16 \
  -c 16384 \
  -fa on \
  --image-min-tokens 256 \
  --image-max-tokens 1024 \
  -v
