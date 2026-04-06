#!/bin/bash
IMAGE_PATH=~/.godex/models/vision/test.png

cat > /tmp/test_vision.json << 'EOF'
{
  "model": "gemma-4",
  "prompt": "<start_of_turn>user\n<image>\nDescribe this image<end_of_turn>\n<start_of_turn>model\n",
  "multimodal_data": ["FILEPATH:/home/cheikh-seck/.godex/models/vision/test.png"],
  "n_predict": 200
}
EOF

echo "=== Testing /completion with FILEPATH ==="
curl -s -X POST http://127.0.0.1:18888/completion -H "Content-Type: application/json" -d @/tmp/test_vision.json
