pub fn greet() -> String {
    format!("{} via mid", leaf::hello())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_greet() {
        assert_eq!(greet(), "hello from leaf via mid");
    }
}
