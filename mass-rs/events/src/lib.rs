//! Fixed commit/publication core and its bounded Handler lanes share ownership.
mod events;
pub mod handlers;
pub use events::Events;
