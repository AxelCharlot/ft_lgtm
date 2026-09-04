//! A short tour of Rust, loaded when the playground opens.

use std::collections::HashMap;
use std::fmt;

/// A shape, described by one of three variants that each carry their own data.
#[derive(Debug, Clone, Copy)]
enum Shape {
    Circle { radius: f64 },
    Rectangle { width: f64, height: f64 },
    Triangle { base: f64, height: f64 },
}

impl Shape {
    /// Every variant answers this differently, and the compiler checks that
    /// none of them was forgotten.
    fn area(&self) -> f64 {
        match self {
            Shape::Circle { radius } => std::f64::consts::PI * radius * radius,
            Shape::Rectangle { width, height } => width * height,
            Shape::Triangle { base, height } => base * height / 2.0,
        }
    }

    fn name(&self) -> &'static str {
        match self {
            Shape::Circle { .. } => "circle",
            Shape::Rectangle { .. } => "rectangle",
            Shape::Triangle { .. } => "triangle",
        }
    }
}

/// Implementing Display is what lets `{}` print a Shape.
impl fmt::Display for Shape {
    fn fmt(&self, formatter: &mut fmt::Formatter) -> fmt::Result {
        write!(formatter, "{:<10} {:>8.2}", self.name(), self.area())
    }
}

fn main() {
    let shapes = vec![
        Shape::Circle { radius: 1.5 },
        Shape::Rectangle {
            width: 3.0,
            height: 4.0,
        },
        Shape::Triangle {
            base: 6.0,
            height: 2.5,
        },
        Shape::Circle { radius: 0.5 },
    ];

    println!("shape          area");
    for shape in &shapes {
        println!("{shape}");
    }

    let total: f64 = shapes.iter().map(Shape::area).sum();
    println!("{:<10} {:>8.2}", "total", total);

    let largest = shapes
        .iter()
        .max_by(|left, right| left.area().total_cmp(&right.area()))
        .expect("the list is never empty");
    println!("largest is a {}", largest.name());

    let mut counts: HashMap<&str, usize> = HashMap::new();
    for shape in &shapes {
        *counts.entry(shape.name()).or_insert(0) += 1;
    }

    let mut kinds: Vec<(&str, usize)> = counts.into_iter().collect();
    kinds.sort();
    println!("kinds: {kinds:?}");
}
