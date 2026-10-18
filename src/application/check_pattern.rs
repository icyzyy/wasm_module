use crate::domain::{InvalidPattern, Pattern};

/// Use case: checks whether `text` matches the regular expression `pattern`.
///
/// A thin delegation to [`domain::Pattern`] that keeps the outer (Wasm/FFI)
/// layer from depending on the domain directly. Returns `Ok(true)` or
/// `Ok(false)` when the pattern is valid, or the domain's [`InvalidPattern`]
/// error otherwise.
///
/// # Errors
///
/// Returns [`InvalidPattern`] if `pattern` is not a syntactically valid
/// regular expression.
pub fn check_pattern(pattern: &str, text: &str) -> Result<bool, InvalidPattern> {
    Pattern::new(pattern).map(|p| p.matches(text))
}

#[cfg(test)]
mod tests {
    use super::check_pattern;

    #[test]
    fn delegates_to_domain_and_returns_bool() {
        assert_eq!(check_pattern("^ab.*", "abcdef"), Ok(true));
        assert_eq!(check_pattern("^ab.*", "xyz"), Ok(false));
    }
    #[test]
    fn propagates_invalid_pattern() {
        assert!(check_pattern("[[", "x").is_err());
    }
}
