"""Generated protobuf/gRPC code. Regenerate with ``make proto`` from the repo root.

protoc emits absolute imports (``from doppio.worker.v1 import ...``), so this
directory is placed on ``sys.path`` before importing the generated modules.
"""

import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from doppio.worker.v1 import worker_pb2, worker_pb2_grpc  # noqa: E402

__all__ = ["worker_pb2", "worker_pb2_grpc"]
