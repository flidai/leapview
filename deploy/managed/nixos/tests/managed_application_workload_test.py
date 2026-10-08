"""Bounded HTTP workload contracts; no application or Docker fixture is faked."""
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import managed_application_workload as workload


class WorkloadTest(unittest.TestCase):
    def test_real_upload_contract_waits_for_completion_before_receipt(self):
        payload = b"value\n1\n"
        manifest, revision = workload.write_manifest(payload)
        client = object.__new__(workload.Workload)
        client.project = "project:leapview-evaluation"
        client.base = "/api/v1/projects/project:leapview-evaluation/connections/connection:sample"
        session = {"id": "upload-1", "project": client.project, "connection": "connection:sample", "revisionId": revision,
                   "manifest": manifest, "status": "open", "files": [{"file": manifest["files"][0], "negotiation": {
                       "protocol": "tus", "tus": {"endpoint": "/upload-protocols/tus", "uploadId": "blob-1"}}}]}
        replies = [session, {**session, "status": "finalizing"}, {**session, "status": "completed"},
                   {"id": revision, "manifest": manifest, "status": "available"}]
        class Response(io.BytesIO):
            def __init__(self, headers):
                super().__init__()
                self.headers = headers
        head = Response({"Upload-Offset": "0", "Upload-Length": str(len(payload))})
        patched = Response({"Upload-Offset": str(len(payload))})
        with patch.object(client, "_json", side_effect=replies) as api, \
                patch.object(client, "_request", side_effect=[head, patched]) as transfer, \
                patch.object(workload.time, "sleep"):
            self.assertEqual(client.acknowledge_write(payload), {"revisionID": revision, "manifest": manifest})
            self.assertEqual([call.args[0] for call in api.call_args_list], ["POST", "POST", "GET", "GET"])
            self.assertEqual(api.call_args_list[1].args[1], client.base + "/upload-sessions/upload-1/finalize")
            self.assertEqual(transfer.call_args_list[1].args[2], payload)
        self.assertTrue(head.closed and patched.closed)

    def test_manifest_matches_go_canonical_order_and_acknowledgement(self):
        manifest, revision = workload.write_manifest(b"value\n1\n")
        self.assertEqual(list(manifest["files"][0]), ["path", "size", "sha256"])
        client = object.__new__(workload.Workload)
        client.project = "project:leapview-evaluation"
        client.base = "/api/v1/projects/project:leapview-evaluation/connections/connection:sample"
        metadata = {"id": revision, "manifest": manifest, "status": "available", "fileCount": 1, "size": 8}
        receipt = {"revisionID": revision, "manifest": manifest}
        with patch.object(client, "_json", return_value=metadata):
            self.assertEqual(client.verify_acknowledgement(receipt), receipt)
        metadata["manifest"] = {"files": []}
        with patch.object(client, "_json", return_value=metadata):
            with self.assertRaisesRegex(ValueError, "acknowledged"):
                client.verify_acknowledgement(receipt)

    def test_upload_rejects_changed_manifest_and_cross_origin_negotiation(self):
        manifest, revision = workload.write_manifest(b"value\n1\n")
        client = object.__new__(workload.Workload)
        client.project = "project:leapview-evaluation"
        client.base = "/api/v1/projects/project:leapview-evaluation/connections/connection:sample"
        session = {"id": "upload-1", "project": client.project, "connection": "connection:sample", "revisionId": revision,
                   "manifest": manifest, "status": "open", "files": [{"file": manifest["files"][0], "negotiation": {
                       "protocol": "tus", "tus": {"endpoint": "https://other.invalid/upload", "uploadId": "blob-1"}}}]}
        with patch.object(client, "_json", return_value=session), patch.object(client, "_request") as request:
            with self.assertRaisesRegex(ValueError, "upload endpoint"):
                client.acknowledge_write(b"value\n1\n")
            request.assert_not_called()
        session["revisionId"] = "sha256:" + "0" * 64
        with patch.object(client, "_json", return_value=session):
            with self.assertRaisesRegex(ValueError, "upload identity"):
                client.acknowledge_write(b"value\n1\n")

    def test_sse_requires_complete_signal_frame_then_eof(self):
        body = io.BytesIO(b"event: datastar-patch-signals\ndata: signals {}\n\n")
        with workload.SSEStream(body) as stream:
            stream.wait_first_frame()
            stream.wait_drained()
        self.assertTrue(body.closed)
        with workload.SSEStream(io.BytesIO(b"event: datastar-patch-signals\n")) as stream:
            with self.assertRaisesRegex(ValueError, "signal frame"):
                stream.wait_first_frame()
        with workload.SSEStream(io.BytesIO(b"event: datastar-patch-signals\n\n")) as stream:
            with self.assertRaisesRegex(ValueError, "signal frame"):
                stream.wait_first_frame()

    def test_query_comparison_ignores_row_order_but_preserves_values(self):
        client = object.__new__(workload.Workload)
        rows = [{"state": state, "order_count": 6, "revenue": 10} for state in ("SP", "RJ", "MG", "RS")]
        with patch.object(client, "_json", return_value={"rows": rows}):
            original = client.query()
        with patch.object(client, "_json", return_value={"rows": list(reversed(rows))}):
            self.assertEqual(original, client.query())
        rows[0]["revenue"] = 11
        with patch.object(client, "_json", return_value={"rows": rows}):
            self.assertNotEqual(original, client.query())

    def test_redirect_never_forwards_authorization(self):
        with self.assertRaisesRegex(ValueError, "redirect rejected"):
            workload.NoRedirect().redirect_request(None, None, 302, "", {}, "https://other.invalid")

    def test_invalid_private_credential_rejected_before_network_setup(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "credential.json"
            path.write_text(json.dumps({"token": "must-never-appear"}))
            path.chmod(0o644)
            with self.assertRaisesRegex(ValueError, "private credential") as raised:
                workload.Workload(path, "/unused/ca.crt")
            self.assertNotIn("must-never-appear", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
