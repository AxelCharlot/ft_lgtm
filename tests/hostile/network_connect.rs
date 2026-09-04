//! Attacks the network: no socket capability is ever granted to the guest.
//!
//! The refusal is `Unsupported`, because the platform the program was compiled
//! for has no way to open a socket at all. As with the file test, printing the
//! refusal is not enough — the unwrap is what makes the run fail.

use std::net::TcpStream;

fn main() {
    let attempt = TcpStream::connect("1.1.1.1:80");
    println!("the sandbox answered: {attempt:?}");
    attempt.unwrap();
}
