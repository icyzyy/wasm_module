//! Adapters layer: infrastructure that speaks to the outside world.
//!
//! The only layer allowed to be Wasm-aware; depends on `application`. Domain
//! types (such as `InvalidPattern`) may surface here only as they flow
//! through application return types — this layer never reaches past
//! `application` to import from `domain` itself.

pub mod wasm_ffi;
