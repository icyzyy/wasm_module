//! Adapters layer: infrastructure that speaks to the outside world.
//!
//! The only layer allowed to be Wasm-aware; depends on `application`, never
//! on `domain` directly.

pub mod wasm_ffi;
