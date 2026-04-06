#!/bin/bash
cat > /tmp/test_vision.json << 'EOF'
{
  "model": "lightonocr",
  "prompt": "Extract all text from this image:\n",
  "multimodal_data": ["FILEPATH:/home/cheikh-seck/.godex/models/vision/test.png"],
  "n_predict": 1024,
  "temperature": 0.0,
  "top_p": 0.9
}
EOF

curl -s -X POST http://127.0.0.1:18888/completion \
  -H "Content-Type: application/json" \
  -d @/tmp/test_vision.json | jq -r '.content' 2>/dev/null
