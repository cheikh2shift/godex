#!/usr/bin/env python3
"""
DYNO Mock Server - Intercepts all HTTP requests and returns mock responses
to simulate tool calling for godex testing.

Handles both OpenAI and Ollama API formats.
"""

import os
import sys
import json
import logging
import threading
from http.server import HTTPServer, BaseHTTPRequestHandler
from datetime import datetime
from urllib.parse import urlparse
import re

# Configuration
MOCK_PORT = int(os.environ.get('DYNO_MOCK_PORT', 8888))
LOG_FILE = os.environ.get('DYNO_HTTP_LOG', '/dyno/logs/http_tracking.log')
MOCK_MODE = os.environ.get('DYNO_MOCK_MODE', 'true').lower() == 'true'

# Setup logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s [DYNO-MOCK] %(message)s'
)
logger = logging.getLogger('dyno-mock')

# Request tracking
request_log_lock = threading.Lock()

def log_request(method, url, headers, body, response_status, response_body):
    """Log request to tracking file"""
    timestamp = datetime.now().isoformat()
    log_entry = f"[{timestamp}] {method} {url}\n"
    log_entry += f"  Headers: {dict(headers)}\n"
    log_entry += f"  Body: {body[:500] if body else 'None'}\n"
    log_entry += f"  Response: {response_status}\n"
    log_entry += f"  Response Body: {response_body[:500]}\n"
    log_entry += "-" * 80 + "\n"
    
    os.makedirs(os.path.dirname(LOG_FILE), exist_ok=True)
    with request_log_lock:
        with open(LOG_FILE, 'a') as f:
            f.write(log_entry)
    
    logger.info(f"{method} {url} -> {response_status}")


class MockHandler(BaseHTTPRequestHandler):
    """Handler that returns mock responses for all HTTP requests"""
    
    protocol_version = 'HTTP/1.1'
    
    def log_message(self, format, *args):
        logger.debug(format % args)
    
    def get_mock_response(self, method, path, headers=None, body=None):
        """Determine mock response based on request"""
        
        # Ollama endpoints
        if '/api/chat' in path:
            return self.mock_ollama_chat(body)
        if '/api/show' in path:
            return self.mock_ollama_show(body)
        if '/api/generate' in path:
            return self.mock_ollama_generate(body)
        if '/api/tags' in path or '/tags' in path:
            return self.mock_ollama_tags()
        
        # OpenAI endpoints
        if '/v1/chat/completions' in path or 'chat/completions' in path:
            return self.mock_chat_completion(body)
        if '/v1/embeddings' in path or 'embeddings' in path:
            return self.mock_embeddings_response()
        
        # Default: generic success response
        return self.mock_default_response()
    
    def mock_ollama_chat(self, body):
        """Mock Ollama chat response"""
        try:
            req_data = json.loads(body) if body else {}
        except:
            req_data = {}
        
        model = req_data.get('model', 'gpt-4')
        messages = req_data.get('messages', [])
        
        # Build a response
        response = {
            "model": model,
            "message": {
                "role": "assistant",
                "content": "[DYNO MOCK] This is a simulated AI response. All network calls are being tracked and mocked for testing purposes."
            },
            "done": True
        }
        return 200, response
    
    def mock_ollama_show(self, body):
        """Mock Ollama show response"""
        try:
            req_data = json.loads(body) if body else {}
        except:
            req_data = {}
        
        model = req_data.get('name', 'gpt-4')
        
        response = {
            "model": model,
            "modified_at": "2026-01-01T00:00:00Z",
            "size": 0,
            "digest": "mockdigest123"
        }
        return 200, response
    
    def mock_ollama_generate(self, body):
        """Mock Ollama generate response"""
        try:
            req_data = json.loads(body) if body else {}
        except:
            req_data = {}
        
        model = req_data.get('model', 'gpt-4')
        
        response = {
            "model": model,
            "response": "[DYNO MOCK] This is a simulated AI response.",
            "done": True
        }
        return 200, response
    
    def mock_ollama_tags(self):
        """Mock Ollama tags response"""
        response = {
            "models": [
                {
                    "name": "gpt-4",
                    "modified_at": "2026-01-01T00:00:00Z",
                    "size": 0,
                    "digest": "mockdigest123"
                }
            ]
        }
        return 200, response
    
    def mock_chat_completion(self, body):
        """Mock OpenAI chat completion"""
        try:
            req_data = json.loads(body) if body else {}
        except:
            req_data = {}
        
        response = {
            "id": "chatcmpl-mock-dyno-001",
            "object": "chat.completion",
            "created": int(datetime.now().timestamp()),
            "model": req_data.get('model', 'gpt-4'),
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": "[DYNO MOCK] This is a simulated AI response."
                },
                "finish_reason": "stop"
            }],
            "usage": {
                "prompt_tokens": 50,
                "completion_tokens": 50,
                "total_tokens": 100
            }
        }
        return 200, response
    
    def mock_embeddings_response(self):
        """Mock OpenAI embeddings response"""
        response = {
            "object": "list",
            "data": [{
                "object": "embedding",
                "embedding": [0.1] * 1536,
                "index": 0
            }],
            "model": "text-embedding-ada-002",
            "usage": {
                "prompt_tokens": 8,
                "total_tokens": 8
            }
        }
        return 200, response
    
    def mock_default_response(self):
        """Default mock response"""
        response = {
            "status": "mocked",
            "message": "DYNO mock server - all network calls are simulated",
            "timestamp": datetime.now().isoformat()
        }
        return 200, response
    
    def do_GET(self):
        """Handle GET requests"""
        path = self.path
        logger.info(f"GET {path}")
        
        status, response = self.get_mock_response('GET', path)
        
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps(response).encode())
    
    def do_POST(self):
        """Handle POST requests"""
        path = self.path
        content_length = int(self.headers.get('Content-Length', 0))
        body = self.rfile.read(content_length).decode('utf-8') if content_length > 0 else ''
        
        logger.info(f"POST {path}")
        
        status, response = self.get_mock_response('POST', path, dict(self.headers), body)
        
        # Log the request
        log_request('POST', path, dict(self.headers), body, status, json.dumps(response))
        
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps(response).encode())


def main():
    """Start the mock server"""
    logger.info(f"Starting DYNO mock server on port {MOCK_PORT}")
    logger.info(f"Logging to: {LOG_FILE}")
    
    server = HTTPServer(('0.0.0.0', MOCK_PORT), MockHandler)
    logger.info(f"DYNO mock server ready at http://0.0.0.0:{MOCK_PORT}")
    
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        logger.info("Shutting down DYNO mock server")
        server.shutdown()


if __name__ == '__main__':
    main()
