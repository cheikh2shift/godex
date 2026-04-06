#!/bin/bash
# Install tesseract OCR:
# sudo apt-get update && sudo apt-get install -y tesseract-ocr

IMAGE_PATH="$1"
if [ -z "$IMAGE_PATH" ]; then
    echo "Usage: $0 <image_path>"
    exit 1
fi

if ! command -v tesseract &> /dev/null; then
    echo "Error: tesseract not installed. Run: sudo apt-get install -y tesseract-ocr"
    exit 1
fi

tesseract "$IMAGE_PATH" stdout
