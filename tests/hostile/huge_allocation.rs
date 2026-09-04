//! Attacks the memory limit: 10 MiB, which is 160 pages of 64 KiB.
//!
//! `black_box` is what makes this a test rather than a measurement of the
//! optimiser. Without it, `rustc -O` sees that the length is the constant it was
//! handed, deletes the allocation and prints the number, and the run succeeds
//! having asked for nothing. memory.md records that probe fooling us once.
//!
//! A refused allocation has no kind of its own. Rust answers one by printing
//! `memory allocation of N bytes failed` and calling abort(), which compiles to
//! a trap — the same mechanism as any panic — so the backend reports `runtime`
//! and the reason the user needs is already in the output. See k8s/README.md
//! section 4.

use std::hint::black_box;

fn main() {
    let size = black_box(100_000_000);
    let bytes = vec![0u8; size];
    println!("{}", black_box(bytes.len()));
}
