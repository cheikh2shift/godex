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
request_counter = {"count": 0}

# Tool-call payloads for godex's non-native tool calling loop.
# godex expects one JSON object per markdown ```json``` block, with keys:
#   {"name": "<tool_name>", "arguments": {...}}
#
# Note: DYNO's dummy provider config must also enable MCP servers for these tools
# to actually exist; otherwise godex will log them as "Ignored unknown tool(s)".
DYNO_TOOL_CALL_BATCHES = [
    # Batch 1: independent calls + create/write (read happens later to avoid parallel races).
    [
        {"name": "list_directory", "arguments": {"path": "/dyno/workspace"}},
        {"name": "search_files", "arguments": {"path": "/dyno", "pattern": "*.py"}},
        {"name": "get_file_info", "arguments": {"path": "/dyno/dyno-mock-server.py"}},
        {
            "name": "read_file_line_range",
            "arguments": {"path": "/dyno/dyno-mock-server.py", "start_line": 1, "end_line": 40},
        },
        {"name": "search_in_file", "arguments": {"path": "/dyno/dyno-mock-server.py", "pattern": "def "}},
        {"name": "create_directory", "arguments": {"path": "/dyno/workspace/example_dir"}},
        {
            "name": "write_file",
            "arguments": {
                "path": "/dyno/workspace/example_dir/example.txt",
                "content": "hello from dyno\nsecond line\nthird line\n",
            },
        },
        {"name": "run_command", "arguments": {"command": "ls -la /dyno/workspace"}},
        {"name": "run_python", "arguments": {"code": "print('dyno python ok')"}},
        {"name": "run_node", "arguments": {"code": "console.log('dyno node ok')"}},
        {"name": "run_bash_script", "arguments": {"code": "echo 'dyno bash ok'"}},
        # Webscraper tool that doesn't require a browser: parse links from provided HTML.
        {
            "name": "get_links",
            "arguments": {
                "html": "<html><body><a href='/x'>x</a><a href='https://example.com/y'>y</a></body></html>",
                "base_url": "https://example.com",
            },
        },
    ],
    # Batch 2: dependent read after write_file has completed.
    [
        {"name": "read_file", "arguments": {"path": "/dyno/workspace/example_dir/example.txt"}},
        {
            "name": "replace_first_in_file",
            "arguments": {
                "path": "/dyno/workspace/example_dir/example.txt",
                "find": "hello from dyno",
                "replace": "hello from dyno (updated)",
            },
        },
        {
            "name": "insert_at_line",
            "arguments": {
                "path": "/dyno/workspace/example_dir/example.txt",
                "line": 0,
                "content": "inserted line",
            },
        },
        {
            "name": "search_directory_text",
            "arguments": {"path": "/dyno/workspace", "query": "replaced second line"},
        },
        {
            "name": "search_file_text",
            "arguments": {"path": "/dyno/workspace", "query": "updated"},
        },
    ],
    # Batch 3: cleanup (avoid interfering with Batch 2 reads/modifications).
    [
        {"name": "delete_file", "arguments": {"path": "/dyno/workspace/example_dir/example.txt"}},
        {"name": "list_directory", "arguments": {"path": "/dyno/workspace/example_dir"}},
    ],
]


def render_tool_blocks(tool_calls):
    return "\n".join("```json\n" + json.dumps(tc) + "\n```" for tc in tool_calls)

# All tools currently used by the system
SYSTEM_TOOLS = [
    {
        "type": "function",
        "function": {
            "name": "list_directory",
            "description": "List files in a directory",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "The directory path to list"}
                },
                "required": ["path"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "read_file",
            "description": "Read contents of a file",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "The file path to read"}
                },
                "required": ["path"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "write_file",
            "description": "Write content to a file",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "The file path to write"},
                    "content": {"type": "string", "description": "Content to write"}
                },
                "required": ["path", "content"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "run_command",
            "description": "Run a shell command and return its output",
            "parameters": {
                "type": "object",
                "properties": {
                    "command": {"type": "string", "description": "The command to run"},
                    "background": {"type": "boolean", "description": "Run in background"}
                },
                "required": ["command"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "read_file_line_range",
            "description": "Read a specific range of lines from a file",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "The file path"},
                    "start_line": {"type": "integer", "description": "Start line number"},
                    "end_line": {"type": "integer", "description": "End line number"}
                },
                "required": ["path", "start_line", "end_line"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "search_in_file",
            "description": "Search for text within a specific file",
            "parameters": {
                "type": "object",
                "properties": {
                    "pattern": {"type": "string", "description": "Pattern to search"},
                    "path": {"type": "string", "description": "File path"}
                },
                "required": ["pattern", "path"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "get_file_info",
            "description": "Get information about a file",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "The file path"}
                },
                "required": ["path"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "create_directory",
            "description": "Create a new directory",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Directory path to create"}
                },
                "required": ["path"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "delete_file",
            "description": "Delete a file",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "File path to delete"}
                },
                "required": ["path"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "search_files",
            "description": "Search for files matching a pattern",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Directory to search"},
                    "pattern": {"type": "string", "description": "File pattern"}
                },
                "required": ["path", "pattern"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "run_python",
            "description": "Run a Python script and return its output",
            "parameters": {
                "type": "object",
                "properties": {
                    "code": {"type": "string", "description": "Python code to run"}
                },
                "required": ["code"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "run_node",
            "description": "Run a Node.js script and return its output",
            "parameters": {
                "type": "object",
                "properties": {
                    "code": {"type": "string", "description": "Node.js code to run"}
                },
                "required": ["code"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "fetch_url",
            "description": "Fetch a URL and return rendered HTML",
            "parameters": {
                "type": "object",
                "properties": {
                    "url": {"type": "string", "description": "URL to fetch"}
                },
                "required": ["url"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "replace_first_in_file",
            "description": "Replace the first occurrence of a text string in a file",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "File path"},
                    "old": {"type": "string", "description": "Text to replace"},
                    "new": {"type": "string", "description": "Replacement text"}
                },
                "required": ["path", "old", "new"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "insert_at_line",
            "description": "Insert content at a specific line number",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "File path"},
                    "line": {"type": "integer", "description": "Line number"},
                    "content": {"type": "string", "description": "Content to insert"}
                },
                "required": ["path", "line", "content"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "search_file_text",
            "description": "Search for text content within files in a directory",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Directory path"},
                    "pattern": {"type": "string", "description": "Search pattern"}
                },
                "required": ["path", "pattern"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "search_directory_text",
            "description": "Search for text content within files in a directory",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Directory path"},
                    "pattern": {"type": "string", "description": "Search pattern"}
                },
                "required": ["path", "pattern"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "read_pdf",
            "description": "Extract text from a PDF file",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "PDF file path"},
                    "pages": {"type": "string", "description": "Page range (e.g., '1-5')"}
                },
                "required": ["path"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "read_text",
            "description": "Extract text from an image using OCR",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Image file path"}
                },
                "required": ["path"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "get_links",
            "description": "Extract all links from HTML content",
            "parameters": {
                "type": "object",
                "properties": {
                    "url": {"type": "string", "description": "URL to fetch"}
                },
                "required": ["url"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "list_connected_tabs",
            "description": "List all tabs connected via Chrome extension",
            "parameters": {
                "type": "object",
                "properties": {}
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "get_page_source",
            "description": "Get raw HTML source from a Chrome tab",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"}
                },
                "required": ["tab_id"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "extract_page_content",
            "description": "Extract readable text, links, forms, images from page",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"}
                },
                "required": ["tab_id"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "click_element",
            "description": "Click an element on the page using CSS selector",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"},
                    "selector": {"type": "string", "description": "CSS selector"}
                },
                "required": ["tab_id", "selector"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "find_elements",
            "description": "Find DOM elements using CSS/XPath",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"},
                    "selector": {"type": "string", "description": "CSS/XPath selector"}
                },
                "required": ["tab_id", "selector"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "get_visible_elements",
            "description": "Get all visible elements with text content",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"}
                },
                "required": ["tab_id"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "search_text",
            "description": "Search for text within visible page elements",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"},
                    "text": {"type": "string", "description": "Text to search"}
                },
                "required": ["tab_id", "text"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "wait_for_element",
            "description": "Wait for element to appear",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"},
                    "selector": {"type": "string", "description": "CSS selector"}
                },
                "required": ["tab_id", "selector"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "execute_script",
            "description": "Execute JavaScript in a connected tab",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"},
                    "script": {"type": "string", "description": "JavaScript code"}
                },
                "required": ["tab_id", "script"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "get_tab_info",
            "description": "Get info about a connected tab",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"}
                },
                "required": ["tab_id"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "get_element_details",
            "description": "Get detailed element info",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"},
                    "selector": {"type": "string", "description": "CSS selector"}
                },
                "required": ["tab_id", "selector"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "get_page_structure",
            "description": "Get structured DOM overview of the page",
            "parameters": {
                "type": "object",
                "properties": {
                    "tab_id": {"type": "integer", "description": "Tab ID"}
                },
                "required": ["tab_id"]
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "scheduler",
            "description": "Schedule a task to run at a specific time or interval",
            "parameters": {
                "type": "object",
                "properties": {
                    "prompt": {"type": "string", "description": "Prompt to run"},
                    "interval_sec": {"type": "integer", "description": "Interval in seconds"},
                    "run_at": {"type": "string", "description": "Specific time (e.g., '14:30')"},
                    "run_once": {"type": "boolean", "description": "Run only once"}
                },
                "required": ["prompt"]
            }
        }
    }
]


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
        
        # Ollama endpoints (including /v1/api/chat variant)
        if '/api/chat' in path or '/v1/api/chat' in path:
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
        
        # Default
        return self.mock_default()
    
    def mock_ollama_chat(self, body):
        """Mock Ollama chat response with tools support"""
        global request_counter
        
        try:
            req_data = json.loads(body) if body else {}
        except:
            req_data = {}
        
        model = req_data.get('model', 'gpt-4')
        messages = req_data.get('messages', [])
        
        # Check if this is a tool result response
        last_message = messages[-1] if messages else {}
        is_tool_result = last_message.get('role') == 'tool'
        
        # Increment request counter
        request_counter["count"] += 1
        request_num = request_counter["count"]
        
        batch_index = request_num - 1
        if 0 <= batch_index < len(DYNO_TOOL_CALL_BATCHES):
            logger.info(
                f"Request #{request_num} - returning tool batch {batch_index + 1}/{len(DYNO_TOOL_CALL_BATCHES)}"
            )
        else:
            logger.info(f"Request #{request_num} - returning final answer")

        # NOTE: godex's Ollama provider expects streamed JSON objects containing
        # `message.content`. It does not read Ollama-native tool_calls.
        if 0 <= batch_index < len(DYNO_TOOL_CALL_BATCHES):
            tool_calls_content = render_tool_blocks(DYNO_TOOL_CALL_BATCHES[batch_index])
            response = {
                "model": model,
                "created_at": datetime.now().isoformat() + "Z",
                "message": {
                    "role": "assistant",
                    "content": tool_calls_content,
                },
                "done": True,
                "prompt_eval_count": 0,
                "eval_count": 0,
            }
            return 200, response

        # All batches done: return a final answer (the real tool output is handled by godex).
        response = {
            "model": model,
            "created_at": datetime.now().isoformat() + "Z",
            "message": {
                "role": "assistant",
                "content": (
                    "I executed the requested tool calls and am ready to answer based on the results."
                ),
            },
            "done": True,
            "prompt_eval_count": 0,
            "eval_count": 0,
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
        """Mock OpenAI chat completion with tools support"""
        global request_counter
        
        try:
            req_data = json.loads(body) if body else {}
        except:
            req_data = {}
        
        model = req_data.get('model', 'gpt-4')
        messages = req_data.get('messages', [])
        
        # Check if this is a tool result response
        last_message = messages[-1] if messages else {}
        is_tool_result = last_message.get('role') == 'tool'
        
        # Increment request counter
        request_counter["count"] += 1
        request_num = request_counter["count"]
        
        logger.info(f"Request #{request_num} - Tool result: {is_tool_result}")
        
        if not is_tool_result and request_num == 1:
            # First request: return all tools
            response = {
                "id": "chatcmpl-mock-dyno-001",
                "object": "chat.completion",
                "created": int(datetime.now().timestamp()),
                "model": model,
                "choices": [{
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": "",
                        "tool_calls": [
                            {
                                "id": f"call_{i+1}",
                                "type": "function",
                                "function": {
                                    "name": SYSTEM_TOOLS[i]["function"]["name"],
                                    "arguments": json.dumps({"path": "/home/cheikh-seck/godex/dyno"})
                                }
                            }
                            for i in range(len(SYSTEM_TOOLS))
                        ]
                    },
                    "finish_reason": "tool_calls"
                }],
                "usage": {
                    "prompt_tokens": 50,
                    "completion_tokens": 50,
                    "total_tokens": 100
                }
            }
        elif is_tool_result:
            # Tool result received - continue with more tools or final answer
            tool_results = [m for m in messages if m.get('role') == 'tool']
            
            if len(tool_results) >= len(SYSTEM_TOOLS):
                # All tools executed - return final answer
                response = {
                    "id": f"chatcmpl-mock-dyno-{request_num}",
                    "object": "chat.completion",
                    "created": int(datetime.now().timestamp()),
                    "model": model,
                    "choices": [{
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": "Here are the files in the dyno directory:\n- .gitignore\n- .gtree\n- Dockerfile\n- README.md\n- dummy-provider.yaml\n- dyno-entrypoint.sh\n- dyno-mock-server.py\n- dyno-run.sh\n- dyno.env\n\nd logs/\nd workspace/\n\nI have successfully called all 32 system tools and compiled the results."
                        },
                        "finish_reason": "stop"
                    }],
                    "usage": {
                        "prompt_tokens": 50,
                        "completion_tokens": 100,
                        "total_tokens": 150
                    }
                }
            else:
                # Return more tool calls
                remaining_tools = SYSTEM_TOOLS[len(tool_results):]
                response = {
                    "id": f"chatcmpl-mock-dyno-{request_num}",
                    "object": "chat.completion",
                    "created": int(datetime.now().timestamp()),
                    "model": model,
                    "choices": [{
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": "",
                            "tool_calls": [
                                {
                                    "id": f"call_{len(tool_results) + i + 1}",
                                    "type": "function",
                                    "function": {
                                        "name": remaining_tools[i]["function"]["name"],
                                        "arguments": json.dumps({"path": "/home/cheikh-seck/godex/dyno"})
                                    }
                                }
                                for i in range(min(5, len(remaining_tools)))
                            ]
                        },
                        "finish_reason": "tool_calls"
                    }],
                    "usage": {
                        "prompt_tokens": 50,
                        "completion_tokens": 50,
                        "total_tokens": 100
                    }
                }
        else:
            # Second request (no tools): final answer
            response = {
                "id": "chatcmpl-mock-dyno-002",
                "object": "chat.completion",
                "created": int(datetime.now().timestamp()),
                "model": model,
                "choices": [{
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": "Here are the files in the dyno directory:\n- .gitignore\n- .gtree\n- Dockerfile\n- README.md\n- dummy-provider.yaml\n- dyno-entrypoint.sh\n- dyno-mock-server.py\n- dyno-run.sh\n- dyno.env\n\nd logs/\nd workspace/"
                    },
                    "finish_reason": "stop"
                }],
                "usage": {
                    "prompt_tokens": 50,
                    "completion_tokens": 100,
                    "total_tokens": 150
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
    
    def mock_default(self):
        """Mock default response"""
        response = {
            "status": "ok",
            "message": "DYNO mock server is running",
            "timestamp": datetime.now().isoformat()
        }
        return 200, response
    
    def do_GET(self):
        """Handle GET requests"""
        path = self.path
        logger.info(f"GET {path}")
        
        status, response = self.get_mock_response('GET', path)
        
        response_bytes = json.dumps(response).encode("utf-8")
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header("Content-Length", str(len(response_bytes)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(response_bytes)
        self.wfile.flush()
    
    def do_POST(self):
        """Handle POST requests"""
        path = self.path
        content_length = int(self.headers.get('Content-Length', 0))
        body = self.rfile.read(content_length).decode('utf-8') if content_length > 0 else ''
        
        logger.info(f"POST {path}")
        
        status, response = self.get_mock_response('POST', path, dict(self.headers), body)
        
        # Log the request
        log_request('POST', path, dict(self.headers), body, status, json.dumps(response))
        
        response_bytes = json.dumps(response).encode("utf-8")
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header("Content-Length", str(len(response_bytes)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(response_bytes)
        self.wfile.flush()


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
