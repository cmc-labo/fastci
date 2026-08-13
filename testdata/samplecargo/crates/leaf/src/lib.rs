pub fn hello() -> String {
    "hello from leaf".to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_hello() {
        assert_eq!(hello(), "hello from leaf");
    }
}
