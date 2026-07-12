use carina_audit::{AuditLog, Event, EventType};
use serde_json::json;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

fn main() {
    let events = std::env::args()
        .nth(1)
        .and_then(|value| value.parse::<usize>().ok())
        .filter(|value| *value > 0)
        .unwrap_or(100);
    let workers = std::env::args()
        .nth(2)
        .and_then(|value| value.parse::<usize>().ok())
        .filter(|value| *value > 0)
        .unwrap_or(4)
        .min(events);
    let dir = temp_dir();
    let log = Arc::new(AuditLog::open(&dir, "bench").expect("open audit log"));
    let latencies = Arc::new(Mutex::new(Vec::with_capacity(events)));
    let wall = Instant::now();
    let mut handles = Vec::with_capacity(workers);

    for worker in 0..workers {
        let log = Arc::clone(&log);
        let latencies = Arc::clone(&latencies);
        handles.push(std::thread::spawn(move || {
            let mut local = Vec::new();
            for sequence in (worker..events).step_by(workers) {
                let started = Instant::now();
                log.append(&Event::new(
                    "bench",
                    EventType::TaskCreated,
                    json!({"worker": worker, "sequence": sequence}),
                ))
                .expect("append audit event");
                local.push(started.elapsed());
            }
            latencies.lock().expect("latency lock").extend(local);
        }));
    }
    for handle in handles {
        handle.join().expect("audit benchmark worker");
    }

    let elapsed = wall.elapsed();
    let mut samples = latencies.lock().expect("latency lock").clone();
    samples.sort_unstable();
    let p50 = percentile(&samples, 50);
    let p99 = percentile(&samples, 99);
    let report = log.verify().expect("verify audit chain");
    assert!(report.ok, "audit benchmark broke the hash chain");
    assert_eq!(report.event_count, events);
    println!(
        "{}",
        json!({
            "events": events,
            "workers": workers,
            "wall_ms": elapsed.as_millis(),
            "events_per_second": events as f64 / elapsed.as_secs_f64(),
            "p50_ms": duration_ms(p50),
            "p99_ms": duration_ms(p99),
        })
    );
    drop(log);
    std::fs::remove_dir_all(dir).expect("remove audit benchmark directory");
}

fn percentile(samples: &[Duration], percentile: usize) -> Duration {
    let index = ((samples.len() - 1) * percentile).div_ceil(100);
    samples[index]
}

fn duration_ms(duration: Duration) -> f64 {
    duration.as_secs_f64() * 1000.0
}

fn temp_dir() -> PathBuf {
    let nonce = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("system time")
        .as_nanos();
    std::env::temp_dir().join(format!(
        "carina-audit-bench-{}-{nonce:x}",
        std::process::id()
    ))
}
