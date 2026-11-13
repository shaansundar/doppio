from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class HealthRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class HealthResponse(_message.Message):
    __slots__ = ("worker_version", "device", "torch_version", "stages")
    WORKER_VERSION_FIELD_NUMBER: _ClassVar[int]
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    TORCH_VERSION_FIELD_NUMBER: _ClassVar[int]
    STAGES_FIELD_NUMBER: _ClassVar[int]
    worker_version: str
    device: str
    torch_version: str
    stages: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, worker_version: _Optional[str] = ..., device: _Optional[str] = ..., torch_version: _Optional[str] = ..., stages: _Optional[_Iterable[str]] = ...) -> None: ...

class ModelSpec(_message.Message):
    __slots__ = ("id", "family", "hf_repo", "revision", "filename", "memory_gb")
    ID_FIELD_NUMBER: _ClassVar[int]
    FAMILY_FIELD_NUMBER: _ClassVar[int]
    HF_REPO_FIELD_NUMBER: _ClassVar[int]
    REVISION_FIELD_NUMBER: _ClassVar[int]
    FILENAME_FIELD_NUMBER: _ClassVar[int]
    MEMORY_GB_FIELD_NUMBER: _ClassVar[int]
    id: str
    family: str
    hf_repo: str
    revision: str
    filename: str
    memory_gb: float
    def __init__(self, id: _Optional[str] = ..., family: _Optional[str] = ..., hf_repo: _Optional[str] = ..., revision: _Optional[str] = ..., filename: _Optional[str] = ..., memory_gb: _Optional[float] = ...) -> None: ...

class RunStageRequest(_message.Message):
    __slots__ = ("stage", "inputs", "params_json", "model", "out_dir")
    class InputsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    STAGE_FIELD_NUMBER: _ClassVar[int]
    INPUTS_FIELD_NUMBER: _ClassVar[int]
    PARAMS_JSON_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    OUT_DIR_FIELD_NUMBER: _ClassVar[int]
    stage: str
    inputs: _containers.ScalarMap[str, str]
    params_json: str
    model: ModelSpec
    out_dir: str
    def __init__(self, stage: _Optional[str] = ..., inputs: _Optional[_Mapping[str, str]] = ..., params_json: _Optional[str] = ..., model: _Optional[_Union[ModelSpec, _Mapping]] = ..., out_dir: _Optional[str] = ...) -> None: ...

class RunStageResponse(_message.Message):
    __slots__ = ("progress", "result")
    PROGRESS_FIELD_NUMBER: _ClassVar[int]
    RESULT_FIELD_NUMBER: _ClassVar[int]
    progress: StageProgress
    result: StageResult
    def __init__(self, progress: _Optional[_Union[StageProgress, _Mapping]] = ..., result: _Optional[_Union[StageResult, _Mapping]] = ...) -> None: ...

class StageProgress(_message.Message):
    __slots__ = ("fraction", "message")
    FRACTION_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    fraction: float
    message: str
    def __init__(self, fraction: _Optional[float] = ..., message: _Optional[str] = ...) -> None: ...

class Artifact(_message.Message):
    __slots__ = ("name", "rel_path", "media_type", "shape", "dtype")
    NAME_FIELD_NUMBER: _ClassVar[int]
    REL_PATH_FIELD_NUMBER: _ClassVar[int]
    MEDIA_TYPE_FIELD_NUMBER: _ClassVar[int]
    SHAPE_FIELD_NUMBER: _ClassVar[int]
    DTYPE_FIELD_NUMBER: _ClassVar[int]
    name: str
    rel_path: str
    media_type: str
    shape: _containers.RepeatedScalarFieldContainer[int]
    dtype: str
    def __init__(self, name: _Optional[str] = ..., rel_path: _Optional[str] = ..., media_type: _Optional[str] = ..., shape: _Optional[_Iterable[int]] = ..., dtype: _Optional[str] = ...) -> None: ...

class StageResult(_message.Message):
    __slots__ = ("artifacts", "metadata_json", "elapsed_seconds")
    ARTIFACTS_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    ELAPSED_SECONDS_FIELD_NUMBER: _ClassVar[int]
    artifacts: _containers.RepeatedCompositeFieldContainer[Artifact]
    metadata_json: str
    elapsed_seconds: float
    def __init__(self, artifacts: _Optional[_Iterable[_Union[Artifact, _Mapping]]] = ..., metadata_json: _Optional[str] = ..., elapsed_seconds: _Optional[float] = ...) -> None: ...

class ListResidentRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ResidentModel(_message.Message):
    __slots__ = ("model_id", "memory_gb", "last_used_unix")
    MODEL_ID_FIELD_NUMBER: _ClassVar[int]
    MEMORY_GB_FIELD_NUMBER: _ClassVar[int]
    LAST_USED_UNIX_FIELD_NUMBER: _ClassVar[int]
    model_id: str
    memory_gb: float
    last_used_unix: float
    def __init__(self, model_id: _Optional[str] = ..., memory_gb: _Optional[float] = ..., last_used_unix: _Optional[float] = ...) -> None: ...

class ListResidentResponse(_message.Message):
    __slots__ = ("models",)
    MODELS_FIELD_NUMBER: _ClassVar[int]
    models: _containers.RepeatedCompositeFieldContainer[ResidentModel]
    def __init__(self, models: _Optional[_Iterable[_Union[ResidentModel, _Mapping]]] = ...) -> None: ...

class UnloadModelRequest(_message.Message):
    __slots__ = ("model_id",)
    MODEL_ID_FIELD_NUMBER: _ClassVar[int]
    model_id: str
    def __init__(self, model_id: _Optional[str] = ...) -> None: ...

class UnloadModelResponse(_message.Message):
    __slots__ = ("unloaded",)
    UNLOADED_FIELD_NUMBER: _ClassVar[int]
    unloaded: bool
    def __init__(self, unloaded: _Optional[bool] = ...) -> None: ...
