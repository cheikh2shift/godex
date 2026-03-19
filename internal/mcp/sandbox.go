package mcp

import (
	"fmt"
	"strings"
)

func (s *BashServer) pythonSecurityWrapper() string {
	allowedPathsJSON := "[" + strings.Join(func() []string {
		var result []string
		for _, p := range s.allowedPaths {
			result = append(result, fmt.Sprintf("%q", p))
		}
		return result
	}(), ",") + "]"

	return fmt.Sprintf(`
import sys
import os

_ALLOWED_PATHS = %s

def _check_path(path):
    if path is None:
        return True
    path = os.path.abspath(os.path.expanduser(str(path)))
    for allowed in _ALLOWED_PATHS:
        allowed_abs = os.path.abspath(os.path.expanduser(allowed))
        if path == allowed_abs or path.startswith(allowed_abs + os.sep):
            return True
    return False

def _secure_check(name, path):
    if not _check_path(path):
        raise PermissionError("Access denied: " + str(path) + " not in allowed paths: " + str(_ALLOWED_PATHS))

_orig_open = open

class _SecureOpen:
    def __call__(self, file, mode='r', *args, **kwargs):
        _secure_check('open', file)
        return _orig_open(file, mode, *args, **kwargs)
    def __enter__(self):
        return self
    def __exit__(self, *args):
        pass

_orig_os_path_exists = os.path.exists
_orig_os_path_isdir = os.path.isdir
_orig_os_path_isfile = os.path.isfile
_orig_os_listdir = os.listdir
_orig_os_makedirs = os.makedirs
_orig_os_mkdir = os.mkdir
_orig_os_remove = os.remove
_orig_os_rmdir = os.rmdir
_orig_os_unlink = os.unlink
_orig_os_stat = os.stat
_orig_os_walk = os.walk
_orig_os_getcwd = os.getcwd
_orig_os_chdir = os.chdir
_orig_os_access = os.access

class _SecurePath:
    @staticmethod
    def exists(path):
        _secure_check('os.path.exists', path)
        return _orig_os_path_exists(path)
    @staticmethod
    def isdir(path):
        _secure_check('os.path.isdir', path)
        return _orig_os_path_isdir(path)
    @staticmethod
    def isfile(path):
        _secure_check('os.path.isfile', path)
        return _orig_os_path_isfile(path)

class _SecureOs:
    @staticmethod
    def exists(path):
        _secure_check('os.exists', path)
        return _orig_os_path_exists(path)
    @staticmethod
    def isdir(path):
        _secure_check('os.isdir', path)
        return _orig_os_path_isdir(path)
    @staticmethod
    def isfile(path):
        _secure_check('os.isfile', path)
        return _orig_os_path_isfile(path)
    @staticmethod
    def listdir(path='.'):
        _secure_check('os.listdir', path)
        return _orig_os_listdir(path)
    @staticmethod
    def makedirs(path, mode=0o777, exist_ok=False):
        _secure_check('os.makedirs', path)
        return _orig_os_makedirs(path, mode, exist_ok)
    @staticmethod
    def mkdir(path, mode=0o777):
        _secure_check('os.mkdir', path)
        return _orig_os_mkdir(path, mode)
    @staticmethod
    def remove(path):
        _secure_check('os.remove', path)
        return _orig_os_remove(path)
    @staticmethod
    def rmdir(path):
        _secure_check('os.rmdir', path)
        return _orig_os_rmdir(path)
    @staticmethod
    def unlink(path):
        _secure_check('os.unlink', path)
        return _orig_os_unlink(path)
    @staticmethod
    def stat(path, *, dir_fd=None, follow_symlinks=True):
        _secure_check('os.stat', path)
        return _orig_os_stat(path, dir_fd=dir_fd, follow_symlinks=follow_symlinks)
    @staticmethod
    def walk(top, topdown=True, onerror=None, followlinks=False):
        _secure_check('os.walk', top)
        return _orig_os_walk(top, topdown, onerror, followlinks)
    @staticmethod
    def getcwd():
        return _orig_os_getcwd()
    @staticmethod
    def chdir(path):
        _secure_check('os.chdir', path)
        return _orig_os_chdir(path)
    @staticmethod
    def access(path, mode):
        _secure_check('os.access', path)
        return _orig_os_access(path, mode)

_builtins = __builtins__ if isinstance(__builtins__, dict) else vars(__builtins__)
_builtins['open'] = _SecureOpen()

os.path.exists = _SecurePath.exists
os.path.isdir = _SecurePath.isdir
os.path.isfile = _SecurePath.isfile
os.exists = _SecureOs.exists
os.isdir = _SecureOs.isdir
os.isfile = _SecureOs.isfile
os.listdir = _SecureOs.listdir
os.makedirs = _SecureOs.makedirs
os.mkdir = _SecureOs.mkdir
os.remove = _SecureOs.remove
os.rmdir = _SecureOs.rmdir
os.unlink = _SecureOs.unlink
os.stat = _SecureOs.stat
os.walk = _SecureOs.walk
os.getcwd = _SecureOs.getcwd
os.chdir = _SecureOs.chdir
os.access = _SecureOs.access

_unsafe_modules = frozenset({'os', 'sys', 'subprocess', 'socket', 'urllib', 'http', 'requests', 'aiohttp', 'pty', 'tty', 'termios', 'fcntl', 'resource', 'prctl', 'ctypes', 'cffi', 'winreg', 'msvcrt', 'pathlib', 'importlib', 'imp', 'builtins', 'shutil', 'glob', 'fnmatch'})
_orig_import = _builtins.get('__import__')

def _secure_import(name, *args, **kwargs):
    root = name.split('.')[0]
    if root in _unsafe_modules:
        raise ImportError("Import denied: " + name + " module is blocked for security")
    return _orig_import(name, *args, **kwargs)

_builtins['__import__'] = _secure_import

try:
    import importlib
    _orig_importlib_import = importlib.import_module
    def _secure_importlib(name, *args, **kwargs):
        root = name.split('.')[0]
        if root in _unsafe_modules:
            raise ImportError("Import denied: " + name + " module is blocked for security")
        return _orig_importlib_import(name, *args, **kwargs)
    importlib.import_module = _secure_importlib
except:
    pass

_builtins['exec'] = lambda *args, **kwargs: (_ for _ in ()).throw(RuntimeError("exec is disabled for security"))
_builtins['eval'] = lambda *args, **kwargs: (_ for _ in ()).throw(RuntimeError("eval is disabled for security"))
_builtins['compile'] = lambda *args, **kwargs: (_ for _ in ()).throw(RuntimeError("compile is disabled for security"))
_builtins['execfile'] = lambda *args, **kwargs: (_ for _ in ()).throw(RuntimeError("execfile is disabled for security"))
_builtins['__builtins__'] = _builtins
`, allowedPathsJSON)
}

func (s *BashServer) nodeSecurityWrapper() string {
	allowedPathsJSON := "[" + strings.Join(func() []string {
		var result []string
		for _, p := range s.allowedPaths {
			result = append(result, fmt.Sprintf("%q", p))
		}
		return result
	}(), ",") + "]"

	return fmt.Sprintf(`
(function() {
var _fs = typeof fs !== 'undefined' ? fs : require('fs');
var _path = typeof path !== 'undefined' ? path : require('path');
var _ALLOWED_PATHS = %s;
var _unsafeModules = {};
['child_process', 'exec', 'spawn', 'fork', 'execSync', 'execFileSync', 'spawnSync',
 'dgram', 'net', 'tls', 'http', 'https', 'ws', 'wss', 'xmlhttprequest',
 'socket', 'querystring', 'dns', 'domain', 'cluster', 'repl', 'readline',
 'stream', 'zlib', 'crypto', 'random', 'perf_hooks', 'v8', 'worker_threads',
 'module', 'vm', 'console'].forEach(function(m) { _unsafeModules[m] = true; });

function _checkPath(p) {
    if (!p) return true;
    var absPath = _path.resolve(process.cwd(), String(p));
    for (var i = 0; i < _ALLOWED_PATHS.length; i++) {
        var allowed = _ALLOWED_PATHS[i];
        var absAllowed = _path.resolve(process.cwd(), allowed);
        if (absPath === absAllowed || absPath.startsWith(absAllowed + _path.sep)) {
            return true;
        }
    }
    return false;
}

function _checkAccess(p) {
    if (!_checkPath(p)) {
        throw new Error("Access denied: '" + p + "' is not in allowed paths");
    }
}

var _origFs = {};
var _fsKeys = ['readFile', 'readFileSync', 'writeFile', 'writeFileSync', 'readdir',
    'readdirSync', 'mkdir', 'mkdirSync', 'rmdir', 'rmdirSync', 'unlink',
    'unlinkSync', 'stat', 'statSync', 'exists', 'existsSync', 'access',
    'accessSync', 'createReadStream', 'createWriteStream', 'watch',
    'realpath', 'realpathSync', 'copyFile', 'copyFileSync', 'rename',
    'renameSync', 'link', 'linkSync', 'symlink', 'symlinkSync', 'readlink',
    'readlinkSync', 'lstat', 'lstatSync', 'appendFile', 'appendFileSync'];
_fsKeys.forEach(function(key) {
    if (_fs[key]) {
        _origFs[key] = _fs[key];
    }
});
_fsKeys.forEach(function(key) {
    if (_fs[key] && _origFs[key]) {
        (function(origKey) {
            _fs[origKey] = function() {
                var args = Array.prototype.slice.call(arguments);
                if (args[0] !== undefined) _checkAccess(args[0]);
                return _origFs[origKey].apply(_fs, args);
            };
        })(key);
    }
});

var Module = require('module');
var _origLoad = Module._load;
Module._load = function(name, parent, isMain) {
    var root = name.split('/')[0].split('\\')[0];
    if (_unsafeModules[name] || _unsafeModules[root]) {
        throw new Error("Module '" + name + "' is blocked for security");
    }
    var result = _origLoad.apply(this, arguments);
    if ((name === 'fs' || root === 'fs') && result && typeof result === 'object') {
        var wrapped = {};
        _fsKeys.forEach(function(key) {
            if (typeof result[key] === 'function') {
                (function(k) {
                    wrapped[k] = function() {
                        var args = Array.prototype.slice.call(arguments);
                        if (args[0] !== undefined) _checkAccess(args[0]);
                        return result[k].apply(result, args);
                    };
                })(key);
            } else {
                wrapped[key] = result[key];
            }
        });
        return wrapped;
    }
    return result;
};

var _origCache = Object.assign({}, require.cache);
Object.defineProperty(require, 'cache', {
    get: function() { return _origCache; },
    set: function() { throw new Error("Modifying require.cache is disabled"); }
});

global.eval = function() { throw new Error("eval() is disabled for security"); };
global.Function = function() { throw new Error("Function() is disabled for security"); };

Object.defineProperty(process, 'exit', { value: function() { throw new Error("process.exit() is disabled"); }});
Object.defineProperty(process, 'kill', { value: function() { throw new Error("process.kill() is disabled"); }});
Object.defineProperty(process, 'binding', { value: function() { throw new Error("process.binding() is disabled"); }});
Object.defineProperty(process, 'dlopen', { value: function() { throw new Error("process.dlopen() is disabled"); }});
})();
`, allowedPathsJSON)
}
