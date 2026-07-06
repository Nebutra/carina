;; hello-plugin — Carina example WASM plugin (PRD §8.7).
;;
;; Demonstrates the capability boundary: the plugin requests one capability
;; it declared (command_exec / "go test ./...") and one it did NOT declare
;; (secret / "API_KEY"). The runtime allows the first and refuses the second,
;; recording both decisions in the session audit log.
;;
;; Build to wasm:  wat2wasm hello.wat -o hello.wasm
;; (or use the carina test harness which compiles WAT in-process)
(module
  (import "env" "carina_request_capability"
    (func $req (param i32 i32 i32 i32) (result i32)))
  (import "env" "carina_log" (func $log (param i32 i32)))
  (memory (export "memory") 1)

  ;; string table
  (data (i32.const 0)   "command_exec")   ;; len 12
  (data (i32.const 16)  "go test ./...")  ;; len 13
  (data (i32.const 32)  "secret")         ;; len 6
  (data (i32.const 48)  "API_KEY")        ;; len 7
  (data (i32.const 64)  "hello-plugin")   ;; len 12

  (func (export "carina_run") (result i32)
    (local $allowed i32)

    ;; declared capability -> allowed
    (if (call $req (i32.const 0) (i32.const 12) (i32.const 16) (i32.const 13))
      (then (local.set $allowed (i32.add (local.get $allowed) (i32.const 1)))))

    ;; undeclared capability -> refused (PolicyViolation)
    (if (call $req (i32.const 32) (i32.const 6) (i32.const 48) (i32.const 7))
      (then (local.set $allowed (i32.add (local.get $allowed) (i32.const 1)))))

    (call $log (i32.const 64) (i32.const 12))
    (local.get $allowed)))  ;; returns 1: only the declared capability ran
