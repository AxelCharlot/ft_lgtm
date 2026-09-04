//! Attacks the file system: the sandbox opens no directory to the guest, so no
//! path exists for it to reach.
//!
//! The refusal arrives as an ordinary `Err`, and a program that only prints it
//! exits zero — the run would then succeed and nothing would be visible. The
//! unwrap turns the refusal into a trap, which the backend reports as `runtime`.
//! The line above it keeps the exact refusal in the output, where a reader needs
//! it: `code: 44` is the WASI number for "no such file", and it is what a guest
//! with no preopened directory always gets.

use std::fs::File;

fn main() {
    let attempt = File::open("/etc/passwd");
    println!("the sandbox answered: {attempt:?}");
    attempt.unwrap();
}
