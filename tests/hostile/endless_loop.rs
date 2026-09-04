//! Attacks the time limit: five seconds of wall clock for one run.
//!
//! `loop {}` is not removed by the optimiser. Rust, unlike C, defines an endless
//! loop with no side effect as an endless loop, so this reaches the sandbox as
//! written. wazero closes the module when the deadline of the context passes,
//! and the backend reports that as `timeout`.

fn main() {
    loop {}
}
