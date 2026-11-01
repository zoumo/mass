#!/usr/bin/env python3
"""Authored stateful protocol peer. UUID echo is an assumption, not native evidence."""
import json
import sys
import uuid

mode, project, receipt = sys.argv[1:4]
state = {"provenance": "synthetic", "evidenceClass": "authored-stateful-peer",
         "nativeLaunched": False, "inputs": [], "clientEOF": False}


def send(value):
    print(json.dumps(value), flush=True)


def read():
    line = sys.stdin.readline()
    if not line:
        raise ValueError("truncated EOF")
    return json.loads(line)


try:
    init = read()
    request_id = init.get("request_id")
    assert isinstance(request_id, str) and request_id
    assert init == {"type": "control_request", "request_id": request_id,
                    "request": {"subtype": "initialize", "hooks": None,
                                "forwardSubagentText": True}}, init
    send({"type": "control_response", "response": {"subtype": "success",
          "request_id": request_id, "response": {}}})
    for index, (prompt, reply) in enumerate([
            ("Reply with the word Ready.", "Ready"),
            ("Reply with the word Done.", "Done")]):
        actual = read()
        expected = {"type": "user", "message": {"role": "user", "content": prompt},
                    "session_id": "", "parent_tool_use_id": None}
        link = None
        if index:
            link = actual.get("uuid")
            assert isinstance(link, str) and str(uuid.UUID(link)) == link
            expected["uuid"] = link
        assert actual == expected, actual
        state["inputs"].append(actual)
        if not index:
            send({"type": "system", "subtype": "init", "session_id": "claude-session",
                  "cwd": project, "model": "synthetic", "tools": []})
        if mode == "peer-error":
            raise ValueError("injected authored peer failure")
        if mode == "timeout":
            interrupt = read()
            interrupt_id = interrupt.get("request_id")
            assert isinstance(interrupt_id, str) and interrupt_id
            assert interrupt == {"type": "control_request", "request_id": interrupt_id,
                                 "request": {"subtype": "interrupt"}}, interrupt
            send({"type": "control_response", "response": {"subtype": "success",
                  "request_id": interrupt_id, "response": {}}})
            send({"type": "result", "subtype": "success", "is_error": False,
                  "session_id": "claude-session", "result": "cancelled"})
            assert not sys.stdin.read(), "extra client input after interrupt"
            state["clientEOF"] = True
            break
        if mode == "wrong":
            reply = "not the expected reply"
        if mode == "empty":
            reply = ""
        if mode == "stale" and index:
            reply = "Ready"
        message = {"type": "assistant", "session_id": "claude-session",
                   "message": {"id": "message-" + str(index), "role": "assistant",
                               "content": [{"type": "text", "text": reply}]}}
        result = {"type": "result", "subtype": "success", "is_error": False,
                  "session_id": "claude-session", "uuid": "result-" + str(index),
                  "result": reply}
        if link:
            message["user_message_uuid"] = link
            result["user_message_uuid"] = link
            if mode == "wrong-link":
                message["user_message_uuid"] = str(uuid.UUID(int=1))
                result["user_message_uuid"] = str(uuid.UUID(int=1))
            if mode == "wrong-identity":
                message["session_id"] = "foreign-session"
                result["session_id"] = "foreign-session"
        send(message)
        if mode == "extra":
            message["message"]["id"] += "-extra"
            send(message)
        send(result)
    else:
        line = sys.stdin.readline()
        if line and mode in ("wrong-link", "wrong-identity"):
            interrupt = json.loads(line)
            interrupt_id = interrupt.get("request_id")
            assert isinstance(interrupt_id, str) and interrupt_id
            assert interrupt == {"type": "control_request", "request_id": interrupt_id,
                                 "request": {"subtype": "interrupt"}}, interrupt
            send({"type": "control_response", "response": {"subtype": "success",
                  "request_id": interrupt_id, "response": {}}})
            send({"type": "result", "subtype": "success", "is_error": False,
                  "session_id": "claude-session", "user_message_uuid": link,
                  "result": "cancelled"})
            line = sys.stdin.readline()
        assert not line, "extra client input"
        state["clientEOF"] = True
    state["complete"] = len(state["inputs"]) == 2 and state["clientEOF"]
except Exception as exc:
    state["error"] = str(exc)
    print(str(exc), file=sys.stderr, flush=True)
finally:
    with open(receipt, "w") as out:
        json.dump(state, out, indent=2)
sys.exit(1 if "error" in state else 0)
