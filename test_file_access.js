// Security test script - attempts to access /tmp/foobar.txt
const fs = require('fs');

console.log('=== Node.js File Access Security Test ===');
console.log('Target file: /tmp/foobar.txt\n');

// Attempt 1: Check if file exists
console.log('[1] Checking if file exists...');
try {
    const exists = fs.existsSync('/tmp/foobar.txt');
    console.log('    Result: File exists =', exists);
} catch (err) {
    console.log('    Blocked! Error:', err.message);
}

// Attempt 2: Read file contents
console.log('\n[2] Attempting to read file contents...');
try {
    const content = fs.readFileSync('/tmp/foobar.txt', 'utf8');
    console.log('    SUCCESS! Content:', content.trim());
} catch (err) {
    console.log('    BLOCKED! Error:', err.message);
}

// Attempt 3: Get file stats
console.log('\n[3] Getting file statistics...');
try {
    const stats = fs.statSync('/tmp/foobar.txt');
    console.log('    SUCCESS! Size:', stats.size, 'bytes');
} catch (err) {
    console.log('    BLOCKED! Error:', err.message);
}

// Attempt 4: Read directory listing
console.log('\n[4] Listing /tmp directory...');
try {
    const files = fs.readdirSync('/tmp');
    console.log('    SUCCESS! Contains', files.length, 'files');
    console.log('    Sample:', files.slice(0, 5));
} catch (err) {
    console.log('    BLOCKED! Error:', err.message);
}

console.log('\n=== Test Complete ===');
