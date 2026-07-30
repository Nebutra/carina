use std::io::{self, BufWriter, Write};
use std::sync::mpsc::{self, Receiver, Sender, TryRecvError};

struct Frame {
    sequence: u64,
    bytes: Vec<u8>,
}

#[derive(Debug)]
pub struct TerminalWriter {
    frames: Sender<Frame>,
    acknowledgements: Receiver<u64>,
    buffer: Vec<u8>,
    next_sequence: u64,
    in_flight: Option<u64>,
    framing: bool,
}

impl TerminalWriter {
    pub fn spawn() -> io::Result<Self> {
        Self::spawn_with_writer(io::stdout())
    }

    fn spawn_with_writer(writer: impl Write + Send + 'static) -> io::Result<Self> {
        let (frame_tx, frame_rx) = mpsc::channel::<Frame>();
        let (ack_tx, ack_rx) = mpsc::channel();
        std::thread::Builder::new()
            .name("term-writer".into())
            .spawn(move || {
                let mut writer = BufWriter::with_capacity(64 * 1024, writer);
                while let Ok(frame) = frame_rx.recv() {
                    if writer.write_all(&frame.bytes).is_err() || writer.flush().is_err() {
                        break;
                    }
                    if ack_tx.send(frame.sequence).is_err() {
                        break;
                    }
                }
            })?;
        Ok(Self {
            frames: frame_tx,
            acknowledgements: ack_rx,
            buffer: Vec::with_capacity(64 * 1024),
            next_sequence: 1,
            in_flight: None,
            framing: false,
        })
    }

    pub fn begin_frame(&mut self) -> bool {
        self.poll_acknowledgements();
        if self.in_flight.is_some() {
            return false;
        }
        self.framing = true;
        true
    }

    pub fn end_frame(&mut self) -> io::Result<Option<u64>> {
        self.framing = false;
        if self.buffer == b"\x1b[?2026h\x1b[?2026l" {
            self.buffer.clear();
        }
        self.submit_buffer()
    }

    pub fn abort_frame(&mut self) {
        self.framing = false;
        self.buffer.clear();
    }

    pub fn wait_for_in_flight(&mut self) {
        let Some(target) = self.in_flight else {
            return;
        };
        while let Ok(sequence) = self.acknowledgements.recv() {
            if sequence >= target {
                self.in_flight = None;
                break;
            }
        }
    }

    fn poll_acknowledgements(&mut self) {
        loop {
            match self.acknowledgements.try_recv() {
                Ok(sequence) if self.in_flight.is_some_and(|target| sequence >= target) => {
                    self.in_flight = None;
                }
                Ok(_) => {}
                Err(TryRecvError::Empty | TryRecvError::Disconnected) => break,
            }
        }
    }

    fn submit_buffer(&mut self) -> io::Result<Option<u64>> {
        if self.buffer.is_empty() {
            return Ok(None);
        }
        self.poll_acknowledgements();
        if self.in_flight.is_some() {
            return Err(io::Error::new(
                io::ErrorKind::WouldBlock,
                "terminal frame is still in flight",
            ));
        }
        let sequence = self.next_sequence;
        self.next_sequence = self.next_sequence.wrapping_add(1).max(1);
        let bytes = std::mem::take(&mut self.buffer);
        self.frames
            .send(Frame { sequence, bytes })
            .map_err(|_| io::Error::new(io::ErrorKind::BrokenPipe, "terminal writer stopped"))?;
        self.in_flight = Some(sequence);
        Ok(Some(sequence))
    }
}

impl Write for TerminalWriter {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.buffer.extend_from_slice(bytes);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        if !self.framing {
            self.submit_buffer()?;
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use std::sync::{Arc, Mutex};

    use super::*;

    #[derive(Clone, Default)]
    struct SharedSink(Arc<Mutex<Vec<u8>>>);

    impl Write for SharedSink {
        fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(bytes);
            Ok(bytes.len())
        }

        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    #[test]
    fn empty_frame_has_no_sequence_and_no_in_flight_work() {
        let mut writer = TerminalWriter::spawn().unwrap();
        assert!(writer.begin_frame());
        assert_eq!(writer.end_frame().unwrap(), None);
        assert!(writer.begin_frame());
    }

    #[test]
    fn writer_rejects_a_second_frame_until_sequence_is_acknowledged() {
        let (frame_tx, frame_rx) = mpsc::channel();
        let (ack_tx, ack_rx) = mpsc::channel();
        let mut writer = TerminalWriter {
            frames: frame_tx,
            acknowledgements: ack_rx,
            buffer: Vec::new(),
            next_sequence: 1,
            in_flight: None,
            framing: false,
        };

        assert!(writer.begin_frame());
        writer.write_all(b"frame").unwrap();
        assert_eq!(writer.end_frame().unwrap(), Some(1));
        assert!(!writer.begin_frame());
        let frame = frame_rx.recv().unwrap();
        assert_eq!(frame.sequence, 1);
        assert_eq!(frame.bytes, b"frame");

        ack_tx.send(1).unwrap();
        assert!(writer.begin_frame());
    }

    #[test]
    fn production_writer_acknowledges_only_after_sink_write_and_flush() {
        let sink = SharedSink::default();
        let observed = sink.0.clone();
        let mut writer = TerminalWriter::spawn_with_writer(sink).unwrap();
        assert!(writer.begin_frame());
        writer.write_all(b"atomic-frame").unwrap();
        assert_eq!(writer.end_frame().unwrap(), Some(1));

        writer.wait_for_in_flight();

        assert_eq!(&*observed.lock().unwrap(), b"atomic-frame");
        assert!(writer.begin_frame());
    }

    #[test]
    fn synchronized_envelope_without_frame_bytes_is_discarded() {
        let (frame_tx, frame_rx) = mpsc::channel();
        let (_ack_tx, ack_rx) = mpsc::channel();
        let mut writer = TerminalWriter {
            frames: frame_tx,
            acknowledgements: ack_rx,
            buffer: Vec::new(),
            next_sequence: 1,
            in_flight: None,
            framing: false,
        };
        writer.begin_frame();
        writer.write_all(b"\x1b[?2026h\x1b[?2026l").unwrap();

        assert_eq!(writer.end_frame().unwrap(), None);
        assert!(frame_rx.try_recv().is_err());
    }

    #[test]
    fn framed_terminal_operation_coalesces_internal_flushes() {
        let (frame_tx, frame_rx) = mpsc::channel();
        let (_ack_tx, ack_rx) = mpsc::channel();
        let mut writer = TerminalWriter {
            frames: frame_tx,
            acknowledgements: ack_rx,
            buffer: Vec::new(),
            next_sequence: 1,
            in_flight: None,
            framing: false,
        };

        assert!(writer.begin_frame());
        writer.write_all(b"clear").unwrap();
        writer.flush().unwrap();
        writer.write_all(b"history").unwrap();
        writer.flush().unwrap();
        writer.end_frame().unwrap();

        assert_eq!(frame_rx.recv().unwrap().bytes, b"clearhistory");
    }
}
