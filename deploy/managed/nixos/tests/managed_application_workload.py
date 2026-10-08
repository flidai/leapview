"""Authenticated development lifecycle observations against the real application.

A completed upload is staged, not published. Its acknowledged immutable manifest
is checked separately from queries against the existing published sales graph.
Credentials remain in memory and are never passed to subprocesses or reports.
"""
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import ssl
import stat
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

LIMIT = 1024 * 1024
ACTIONS = {"connection.read", "connection.use", "connection.upload", "source.read",
           "dashboard.read", "semantic.query", "semantic.consume"}


def write_manifest(payload):
    if not isinstance(payload, bytes) or not payload or len(payload) > LIMIT:
        raise ValueError("qualification upload must be bounded nonempty bytes")
    # Preserve Go manageddata.File field order for its canonical revision hash.
    manifest = {"files": [{"path": "managed-lifecycle-ack.csv", "size": len(payload),
                           "sha256": hashlib.sha256(payload).hexdigest()}]}
    canonical = json.dumps(manifest, separators=(",", ":")).encode()
    return manifest, "sha256:" + hashlib.sha256(canonical).hexdigest()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("qualification HTTP redirect rejected")


class SSEStream:
    def __init__(self, response):
        self.response = response

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()

    def close(self):
        self.response.close()

    def wait_first_frame(self):
        total, signal, payload = 0, False, False
        deadline = time.monotonic() + 30
        while total < LIMIT and time.monotonic() < deadline:
            line = self.response.readline(65537)
            total += len(line)
            if not line or len(line) > 65536:
                break
            if line.rstrip(b"\r\n") == b"event: datastar-patch-signals":
                signal = True
            elif line.startswith(b"data: signals "):
                payload = True
            elif line in (b"\n", b"\r\n"):
                if signal and payload:
                    return
                signal, payload = False, False
        raise ValueError("dashboard SSE ended without a complete signal frame")

    def wait_drained(self):
        total, deadline = 0, time.monotonic() + 30
        while total < LIMIT and time.monotonic() < deadline:
            line = self.response.readline(65537)
            if not line:
                return
            total += len(line)
        raise ValueError("dashboard SSE did not drain within observation budget")


class Workload:
    def __init__(self, credential_path, ca_file, project_id="project:leapview-evaluation"):
        path = Path(credential_path)
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o400 or info.st_size > 16384:
            raise ValueError("workload requires a private credential file with mode 0400")
        credential = json.loads(path.read_text())
        try:
            issued = datetime.fromisoformat(credential["issuedAt"].replace("Z", "+00:00"))
            expiry = datetime.fromisoformat(credential["expiresAt"].replace("Z", "+00:00"))
            valid = (credential["projectID"] == project_id and credential["environment"] == "prod"
                     and credential.get("uploadConnectionID") == "connection:sample"
                     and credential["targetURL"] == "https://localhost"
                     and set(credential["actions"]) == ACTIONS and len(credential["actions"]) == len(ACTIONS)
                     and issued <= datetime.now(timezone.utc) < expiry
                     and (expiry - issued).total_seconds() == 7200
                     and isinstance(credential["token"], str) and credential["token"]
                     and "\n" not in credential["token"] and "\r" not in credential["token"])
        except (KeyError, TypeError, ValueError):
            valid = False
        if not valid:
            raise ValueError("workload credential scope or lifetime differs from qualification")
        self.project, self._token = project_id, credential["token"]
        self.base = "/api/v1/projects/" + urllib.parse.quote(project_id, safe=":") + "/connections/connection:sample"
        context = ssl.create_default_context(cafile=str(ca_file))
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                                                  urllib.request.HTTPSHandler(context=context))

    def _request(self, method, path, body=None, headers=None, expected=(200,)):
        parsed = urllib.parse.urlsplit(path)
        if not path.startswith("/") or path.startswith("//") or parsed.scheme or parsed.netloc or parsed.fragment:
            raise ValueError("qualification request must remain on the local HTTPS origin")
        request = urllib.request.Request("https://localhost" + path, data=body, method=method)
        request.add_header("Authorization", "Bearer " + self._token)
        for name, value in (headers or {}).items():
            request.add_header(name, value)
        try:
            response = self._opener.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            error.close()
            raise RuntimeError("qualification HTTP request failed with status " + str(error.code)) from None
        except (OSError, urllib.error.URLError):
            raise RuntimeError("qualification HTTP transport failed") from None
        if response.status not in expected:
            response.close()
            raise ValueError("qualification HTTP response status differs from contract")
        return response

    def _json(self, method, path, body=None, *, key=None, expected=(200,)):
        headers = {"Accept": "application/json"}
        encoded = None
        if body is not None:
            encoded = json.dumps(body, separators=(",", ":")).encode()
            headers["Content-Type"] = "application/json"
        if key:
            headers["Idempotency-Key"] = key
        with self._request(method, path, encoded, headers, expected) as response:
            raw = response.read(LIMIT + 1)
        if len(raw) > LIMIT:
            raise ValueError("qualification JSON response exceeds observation budget")
        return json.loads(raw)

    def acknowledge_write(self, payload):
        manifest, revision = write_manifest(payload)
        session = self._json("POST", self.base + "/upload-sessions", {"manifest": manifest},
                             key="managed-qualification-" + uuid.uuid4().hex, expected=(201,))
        def verify_session(value):
            if (value.get("project") != self.project or value.get("connection") != "connection:sample"
                    or value.get("revisionId") != revision or value.get("manifest") != manifest
                    or not re.fullmatch(r"[A-Za-z0-9_-]+", value.get("id", ""))):
                raise ValueError("acknowledged upload identity differs from request")
        verify_session(session)
        session_path = self.base + "/upload-sessions/" + session["id"]
        if session["status"] != "completed":
            files = session.get("files", [])
            if len(files) != 1 or files[0].get("file") != manifest["files"][0]:
                raise ValueError("upload file negotiation differs from manifest")
            negotiation = files[0].get("negotiation", {})
            if negotiation.get("protocol") != "tus":
                raise ValueError("fresh qualification upload requires the local TUS protocol")
            tus = negotiation.get("tus", {})
            if (tus.get("endpoint") != "/upload-protocols/tus"
                    or not re.fullmatch(r"[A-Za-z0-9_-]+", tus.get("uploadId", ""))):
                raise ValueError("qualification upload endpoint differs from local TUS contract")
            endpoint = tus["endpoint"] + "/" + tus["uploadId"]
            with self._request("HEAD", endpoint, headers={"Tus-Resumable": "1.0.0"}, expected=(200, 204)) as response:
                offset = int(response.headers.get("Upload-Offset", "-1"))
                if offset != 0 or int(response.headers.get("Upload-Length", "-1")) != len(payload):
                    raise ValueError("fresh qualification upload has unexpected offset or length")
            with self._request("PATCH", endpoint, payload, {"Tus-Resumable": "1.0.0", "Upload-Offset": "0",
                                "Content-Type": "application/offset+octet-stream"}, (204,)) as response:
                if int(response.headers.get("Upload-Offset", "-1")) != len(payload):
                    raise ValueError("qualification upload did not acknowledge all bytes")
            session = self._json("POST", session_path + "/finalize", key="managed-finalize-" + uuid.uuid4().hex,
                                 expected=(200, 202))
        deadline = time.monotonic() + 120
        while True:
            verify_session(session)
            if session["id"] != session_path.rsplit("/", 1)[1]:
                raise ValueError("qualification upload session identity changed")
            if session["status"] == "completed":
                break
            if session["status"] not in ("open", "finalizing") or time.monotonic() >= deadline:
                raise ValueError("qualification upload did not complete within its budget")
            time.sleep(.2)
            session = self._json("GET", session_path)
        return self.verify_acknowledgement({"revisionID": revision, "manifest": manifest})

    def verify_acknowledgement(self, receipt):
        revision = receipt["revisionID"]
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", revision):
            raise ValueError("invalid acknowledged revision identity")
        result = self._json("GET", self.base + "/revisions/" + revision)
        if (result.get("id") != revision or result.get("manifest") != receipt["manifest"]
                or result.get("status") != "available"):
            raise ValueError("acknowledged managed revision was not retained exactly")
        return receipt

    def query(self):
        result = self._json("POST", "/api/v1/semantic-models/semantic-model:sales/query", {
            "dimensions": [{"field": "state"}],
            "metrics": [{"field": "order_count"}, {"field": "revenue"}], "limit": 10})
        rows = result.get("rows")
        if not isinstance(rows, list) or len(rows) != 4:
            raise ValueError("published governed sales query differs from four-state fixture")
        # Row order is not guaranteed without orderBy; comparison is semantic.
        canonical = sorted(json.dumps(row, sort_keys=True, separators=(",", ":")) for row in rows)
        return {"rowCount": len(rows), "rowsSHA256": hashlib.sha256("\n".join(canonical).encode()).hexdigest()}

    def open_sse(self):
        response = self._request("GET", "/updates?route=dashboard&dashboard=dashboard:sales-overview&page=overview",
                                 headers={"Accept": "text/event-stream",
                                          "Cookie": "pagestream_client_id=" + str(uuid.uuid4())})
        if response.headers.get_content_type() != "text/event-stream":
            response.close()
            raise ValueError("dashboard response is not an SSE stream")
        return SSEStream(response)
