pub fn isolated() -> String {
    "isolated".to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_isolated() {
        assert_eq!(isolated(), "isolated");
    }
}
