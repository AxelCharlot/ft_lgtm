//! Attacks the output limit: 10 KiB is kept and the rest is counted and dropped.
//!
//! The backend answers `output_limit` and keeps the first 10 240 bytes, followed
//! by a note saying how many bytes were dropped. Output that stops in the middle
//! with no explanation reads like a program that stopped in the middle.

fn main() {
    for line in 0..1_000_000 {
        println!("line {line}");
    }
}
