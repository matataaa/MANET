import json
import os
import mimetypes

from .data.config import WWW_DIR


MIME_TYPES = {
    '.html': 'text/html; charset=utf-8',
    '.css': 'text/css; charset=utf-8',
    '.js': 'application/javascript; charset=utf-8',
    '.svg': 'image/svg+xml',
    '.json': 'application/json; charset=utf-8',
    '.png': 'image/png',
    '.ico': 'image/x-icon',
}


def www_path():
    """Resolve the www directory - check repo-local first, then installed path."""
    local = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), 'www')
    if os.path.isdir(local):
        return local
    return WWW_DIR


WWW = www_path()


def resolve_static(url_path):
    """Map a URL path to a file on disk. Returns (filepath, content_type) or (None, None)."""
    if url_path == '/':
        url_path = '/index.html'

    clean = url_path.lstrip('/')
    filepath = os.path.normpath(os.path.join(WWW, clean))

    if not filepath.startswith(os.path.normpath(WWW)):
        return None, None

    if not os.path.isfile(filepath):
        return None, None

    ext = os.path.splitext(filepath)[1].lower()
    content_type = MIME_TYPES.get(ext, 'application/octet-stream')
    return filepath, content_type
