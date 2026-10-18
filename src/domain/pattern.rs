use regex::Regex;

/// A compiled regular expression validated at construction time.
///
/// Wraps [`regex::Regex`] and rejects malformed patterns during
/// [`Pattern::new`], guaranteeing that every `Pattern` instance is usable.
#[derive(Debug)]
pub struct Pattern {
    regex: Regex,
}

/// Error returned when a pattern string cannot be compiled.
#[derive(Debug, PartialEq, Eq, Clone, Copy)]
pub struct InvalidPattern;

impl std::fmt::Display for InvalidPattern {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("invalid pattern")
    }
}

impl Pattern {
    /// Compiles a regular expression into a validated [`Pattern`].
    ///
    /// # Errors
    ///
    /// Returns [`InvalidPattern`] if `source` is not a syntactically
    /// valid regular expression.
    pub fn new(source: &str) -> Result<Self, InvalidPattern> {
        let regex = Regex::new(source).map_err(|_| InvalidPattern)?;
        Ok(Self { regex })
    }

    /// Returns `true` if the pattern matches anywhere in `text`.
    #[must_use]
    pub fn matches(&self, text: &str) -> bool {
        self.regex.is_match(text)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn matches_simple_prefix() {
        let p = Pattern::new("^ab.*").unwrap();
        assert!(p.matches("abcdef"));
        assert!(!p.matches("xyz"));
    }
    #[test]
    fn invalid_pattern_is_rejected() {
        assert!(Pattern::new("[[").is_err());
    }
    #[test]
    fn empty_pattern_matches_everything() {
        assert!(Pattern::new("").unwrap().matches("anything"));
    }
    #[test]
    fn matching_is_case_sensitive() {
        let p = Pattern::new("ABC").unwrap();
        assert!(!p.matches("abc"));
    }
}
