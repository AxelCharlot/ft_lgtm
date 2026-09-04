//! Does not compile, on purpose. It is the only test here that never reaches the
//! sandbox.
//!
//! The backend answers `compile`, and the page colours a compile error
//! differently from a runtime error, which the subject requires. That is why the
//! kind travels from the backend and is never guessed from the text of the
//! message.
//!
//! rustfmt cannot format this file. A file that does not parse has no formatting.

fn main() {
    let answer: i32 = "not a number"
}
