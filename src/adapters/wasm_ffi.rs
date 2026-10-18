//! Wasm FFI adapter: shared input buffer and the two C-ABI exports.
//!
//! The Wasm ABI contract: the host writes a byte string (concatenated
//! pattern, then text) into the exported static buffer [`IN_BUF`], then calls
//! [`check_regexp_pattern`] with the two byte lengths. Plain Rust, valid on
//! every target; no `#[cfg]` gating.

/// Size in bytes of the shared static input buffer exported to Wasm.
pub const IN_BUF_SIZE: usize = 65536;

/// Shared static input buffer written by the host before every call.
///
/// Layout: the pattern occupies `pattern_len` bytes starting at offset 0,
/// immediately followed by the `text_len` bytes of the text. The host must
/// only ever pass lengths whose sum is at most [`IN_BUF_SIZE`].
#[unsafe(no_mangle)]
pub static mut IN_BUF: [u8; IN_BUF_SIZE] = [0; IN_BUF_SIZE];

/// Checks whether the text matches the regular expression in `IN_BUF`.
///
/// Protocol: the host has written the concatenated pattern and text bytes
/// into [`IN_BUF`]; this export validates the two lengths, decodes both
/// UTF-8 slices, and delegates to `application::check_pattern`.
///
/// Returns one of four result codes:
///
/// * `1`  — the pattern is valid and the text matches it
/// * `0`  — the pattern is valid, but the text does not match
/// * `-1` — the pattern is not a syntactically valid regular expression
/// * `-2` — malformed call: a negative length, a length (or their sum)
///   exceeding [`IN_BUF_SIZE`], or either slice not being valid UTF-8
#[unsafe(no_mangle)]
pub extern "C" fn check_regexp_pattern(pattern_len: i32, text_len: i32) -> i32 {
    // Validation order is part of the Wasm contract; do not reorder.
    if pattern_len < 0 || text_len < 0 {
        return -2;
    }
    if pattern_len > IN_BUF_SIZE as i32
        || text_len > IN_BUF_SIZE as i32
        || pattern_len as i64 + text_len as i64 > IN_BUF_SIZE as i64
    {
        return -2;
    }
    let (pattern_len, text_len) = (pattern_len as usize, text_len as usize);

    // SAFETY: `IN_BUF` is a 65536-byte buffer of initialized memory; the
    // lengths above were validated so that both 0-length prefix slices lie
    // inside it. `addr_of!` reads the pointer without taking any reference
    // to the `static mut` (permitted under the `static_mut_refs` lint).
    let whole = unsafe {
        core::slice::from_raw_parts(core::ptr::addr_of!(IN_BUF).cast::<u8>(), IN_BUF_SIZE)
    };

    // Splitting by validated indices cannot panic (sum <= IN_BUF_SIZE).
    let pattern = match std::str::from_utf8(&whole[..pattern_len]) {
        Ok(p) => p,
        Err(_) => return -2,
    };
    let text = match std::str::from_utf8(&whole[pattern_len..pattern_len + text_len]) {
        Ok(t) => t,
        Err(_) => return -2,
    };

    match crate::application::check_pattern(pattern, text) {
        Ok(true) => 1,
        Ok(false) => 0,
        Err(_) => -1,
    }
}

/// Writes the crate version string into [`IN_BUF`] at offset 0 and returns
/// its length in bytes.
///
/// Protocol: after this call, the first `n` (return value) bytes of
/// [`IN_BUF`] hold the UTF-8 version string, e.g. `"0.1.0"`. Pure adapter
/// metadata — deliberately kept out of `domain`/`application`, which stay
/// about matching.
#[unsafe(no_mangle)]
pub extern "C" fn version() -> i32 {
    let bytes = env!("CARGO_PKG_VERSION").as_bytes();
    // SAFETY: the version string is short ASCII (len("0.1.0") = 5, far
    // below `IN_BUF_SIZE`), so the nonoverlapping copy stays in bounds of
    // the 65536-byte buffer; `addr_of_mut!` forms no reference to the
    // `static mut`.
    unsafe {
        core::ptr::copy_nonoverlapping(
            bytes.as_ptr(),
            core::ptr::addr_of_mut!(IN_BUF).cast::<u8>(),
            bytes.len(),
        );
    }
    bytes.len() as i32
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Copies `pattern` then `text` into the shared `IN_BUF` and returns
    /// their lengths as the `(pattern_len, text_len)` argument pair.
    ///
    /// SAFETY: the two inputs are tiny literals whose combined length is far
    /// below `IN_BUF_SIZE`, so every byte written stays in bounds of the
    /// 65536-byte buffer; the destination pointer comes from `addr_of_mut!`,
    /// so no reference to the `static mut` is ever formed.
    unsafe fn set_input(pattern: &str, text: &str) -> (i32, i32) {
        unsafe {
            let dst = core::ptr::addr_of_mut!(IN_BUF).cast::<u8>();
            core::ptr::copy_nonoverlapping(pattern.as_ptr(), dst, pattern.len());
            core::ptr::copy_nonoverlapping(text.as_ptr(), dst.add(pattern.len()), text.len());
        }
        (pattern.len() as i32, text.len() as i32)
    }

    /// Single test fn: the shared `static mut` buffer must not be touched by
    /// tests running on parallel harness threads.
    #[test]
    fn protocol_matrix() {
        unsafe {
            let (p, t) = set_input("^ab.*", "abcdef");
            assert_eq!(check_regexp_pattern(p, t), 1);
            let (p, t) = set_input("^ab.*", "xyz");
            assert_eq!(check_regexp_pattern(p, t), 0);
            let (p, t) = set_input("[[", "x");
            assert_eq!(check_regexp_pattern(p, t), -1); // invalid pattern
            assert_eq!(check_regexp_pattern(-1, 5), -2); // negative length
            assert_eq!(check_regexp_pattern(65537, 0), -2); // oversized
            assert_eq!(check_regexp_pattern(65536, 1), -2); // sum > buffer
            let (_, t) = set_input("a", "b");
            assert_eq!(check_regexp_pattern(i32::MAX, t), -2); // no overflow panic
            // version(): writes version bytes into IN_BUF, returns length
            let n = version();
            assert!(n > 0);
            // SAFETY: `version()` just wrote `n` bytes (n > 0 and <= buffer
            // size, since it returns the length of the short version string)
            // at offset 0 of the 65536-byte `IN_BUF`; the pointer comes from
            // `addr_of!`, so no reference to the `static mut` is formed. The
            // call itself needs no `unsafe` marker here because the whole
            // test body already runs inside one `unsafe` block.
            let bytes =
                std::slice::from_raw_parts(core::ptr::addr_of!(IN_BUF).cast::<u8>(), n as usize);
            assert_eq!(bytes, env!("CARGO_PKG_VERSION").as_bytes());
        }
    }
}
