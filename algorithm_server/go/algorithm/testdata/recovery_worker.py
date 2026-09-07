"""只用于 Go Worker 生命周期测试；不替代正式算法。

通过文件握手确保测试取消发生在请求已进入 Python 后，避免依赖启动速度。
"""
import json
import os
from pathlib import Path
import sys
import time

if len(sys.argv) > 1:
    Path(sys.argv[1]).touch()
    while True:  # 故意不读 stdin，测试大请求 Write 阻塞时能取消。
        time.sleep(0.01)

for line in sys.stdin:
    envelope = json.loads(line)
    request = envelope["payload"]["request"]
    mode = request.get("mode", "ok")
    if request.get("entered"):
        Path(request["entered"]).touch()
    if mode == "hang":
        while True:
            time.sleep(0.01)
    if mode == "hold":
        while not Path(request["release"]).exists():
            time.sleep(0.01)
    if mode == "exit":
        sys.exit(3)
    if mode == "malformed":
        print("not-json", flush=True)
        continue
    response = {
        "id": "wrong-id" if mode == "mismatch" else envelope["id"],
        "result": {"candidateNodeGroups": [{"pid": os.getpid(), "echo": request.get("echo", "ok")}]},
    }
    if mode == "missing-result":
        response = {"id": envelope["id"]}
    if mode == "business-error":
        response = {"id": envelope["id"], "error": {
            "code": "UNSATISFIED", "message": "test business rejection", "statusCode": 422,
        }}
    print(json.dumps(response), flush=True)
