use crate::providers::{BROWSER_USER_AGENT, ProviderResult, USER_AGENT, error::ProviderError};
use serde::de::DeserializeOwned;
use std::{borrow::Cow, collections::HashMap};
use std::{
    io::Read,
    sync::{Mutex, OnceLock},
    time::{Duration, Instant},
};

static LAST_REQUEST: Mutex<Option<Instant>> = Mutex::new(None);
static SETTINGS: Mutex<(f64, u64, usize)> = Mutex::new((1.0, 15, 2));
pub fn configure(delay: f64, timeout: u64, retries: usize) {
    *SETTINGS.lock().unwrap() = (delay, timeout, retries);
}

// This client only pools connections; every call still performs a fresh HTTP request.
static CLIENT: OnceLock<Result<reqwest::blocking::Client, String>> = OnceLock::new();
const MAX_BODY: u64 = 16 * 1024 * 1024;

fn client() -> ProviderResult<&'static reqwest::blocking::Client> {
    CLIENT
        .get_or_init(|| {
            reqwest::blocking::Client::builder()
                .connect_timeout(Duration::from_secs(10))
                .pool_idle_timeout(Duration::from_secs(90))
                .tcp_keepalive(Duration::from_secs(60))
                .redirect(reqwest::redirect::Policy::limited(10))
                .build()
                .map_err(|error| error.to_string())
        })
        .as_ref()
        .map_err(|error| ProviderError::other(error.clone()))
}

pub struct Http {
    method: &'static str,
    base_url: String,
    params: Vec<(String, String)>,
    headers: HashMap<String, String>,
    body: Vec<u8>,
}

impl Http {
    pub fn get(url: impl Into<String>) -> Self {
        Self::new("GET", url)
    }

    pub fn post(url: impl Into<String>) -> Self {
        Self::new("POST", url)
    }

    fn new(method: &'static str, url: impl Into<String>) -> Self {
        Self {
            method,
            base_url: url.into(),
            params: Vec::new(),
            headers: HashMap::from([("User-Agent".to_string(), USER_AGENT.to_string())]),
            body: Vec::new(),
        }
    }

    pub fn param(mut self, name: &str, value: impl Into<String>) -> Self {
        self.params.push((name.to_string(), value.into()));
        self
    }

    pub fn header(mut self, name: &str, value: impl Into<String>) -> Self {
        self.headers.insert(name.to_string(), value.into());
        self
    }

    pub fn headers(mut self, headers: HashMap<String, String>) -> Self {
        self.headers.extend(headers);
        self
    }

    /// For providers that should not know the real plugin User Agent
    /// and instead use a standard browser one.
    pub fn browser(self) -> Self {
        self.header("User-Agent", BROWSER_USER_AGENT)
    }

    pub fn body(mut self, body: Vec<u8>) -> Self {
        self.body = body;
        self
    }

    pub fn url(&self) -> ProviderResult<String> {
        if self.params.is_empty() {
            return Ok(self.base_url.clone());
        }

        let query = serde_urlencoded::to_string(&self.params)
            .map_err(|e| ProviderError::other(format!("failed to encode the query string: {e}")))?;

        Ok(format!("{}?{query}", self.base_url))
    }

    pub fn send(self) -> ProviderResult<Response> {
        let (delay, timeout, retries) = *SETTINGS.lock().unwrap();
        let url = self.url()?;
        for attempt in 0..=retries {
            {
                let mut last = LAST_REQUEST.lock().unwrap();
                if let Some(instant) = *last {
                    let interval = Duration::from_secs_f64(delay);
                    if instant.elapsed() < interval {
                        std::thread::sleep(interval - instant.elapsed());
                    }
                }
                *last = Some(Instant::now());
            }
            let method = reqwest::Method::from_bytes(self.method.as_bytes())
                .map_err(|error| ProviderError::other(error.to_string()))?;
            let mut request = client()?
                .request(method, &url)
                .timeout(Duration::from_secs(timeout));
            for (name, value) in &self.headers {
                request = request.header(name, value);
            }
            if !self.body.is_empty() {
                request = request.body(self.body.clone());
            }
            let response = request
                .send()
                .map_err(|error| {
                    let invalid_request = error.is_builder();
                    let message = error.without_url().to_string();
                    if invalid_request {
                        ProviderError::other(message)
                    } else {
                        ProviderError::unavailable(message, None)
                    }
                })
                .and_then(|response| {
                    let status = i32::from(response.status().as_u16());
                    let headers = response
                        .headers()
                        .iter()
                        .filter_map(|(name, value)| {
                            value
                                .to_str()
                                .ok()
                                .map(|value| (name.to_string(), value.to_string()))
                        })
                        .collect();
                    if response
                        .content_length()
                        .is_some_and(|size| size > MAX_BODY)
                    {
                        return Err(ProviderError::other("response exceeds 16 MiB"));
                    }
                    let mut body = Vec::new();
                    response
                        .take(MAX_BODY + 1)
                        .read_to_end(&mut body)
                        .map_err(|error| {
                            ProviderError::unavailable(
                                format!("reading HTTP response: {error}"),
                                None,
                            )
                        })?;
                    if body.len() as u64 > MAX_BODY {
                        return Err(ProviderError::other("response exceeds 16 MiB"));
                    }
                    Ok(Response {
                        status,
                        headers,
                        body,
                    })
                });
            match response {
                Ok(response) if response.status < 500 || attempt == retries => return Ok(response),
                Err(error) if attempt == retries || matches!(error, ProviderError::Other(_)) => {
                    return Err(error);
                }
                result => {
                    let backoff = 2u64.pow(attempt.min(4) as u32);
                    let retry_after = match &result {
                        Ok(response) => response.header("Retry-After").and_then(parse_retry_after),
                        Err(_) => None,
                    };
                    // Long server waits belong in the provider cooldown, not a blocking
                    // HTTP retry that holds up every other provider for this track.
                    if retry_after.is_some_and(|seconds| seconds > 60) {
                        return result;
                    }
                    let seconds = retry_after
                        .map(|seconds| seconds.max(1) as u64)
                        .unwrap_or(backoff)
                        .max(backoff);
                    std::thread::sleep(Duration::from_secs(seconds));
                }
            }
        }
        unreachable!()
    }
}

pub struct Response {
    pub status: i32,
    headers: HashMap<String, String>,
    pub body: Vec<u8>,
}

impl Response {
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }

    pub fn text(&self) -> Cow<'_, str> {
        String::from_utf8_lossy(&self.body)
    }

    pub fn json<T: DeserializeOwned>(&self, what: &str) -> ProviderResult<T> {
        serde_json::from_slice(&self.body)
            .map_err(|e| ProviderError::other(format!("failed to parse the {what} response: {e}")))
    }

    pub fn unexpected_status(&self, what: &str) -> ProviderError {
        let message = format!("{what} returned status {}", self.status);
        match self.status {
            429 => self.rate_limited(),
            401 | 403 | 500..=599 => ProviderError::unavailable(
                message,
                self.header("Retry-After").and_then(parse_retry_after),
            ),
            _ => ProviderError::other(message),
        }
    }

    pub fn rate_limited(&self) -> ProviderError {
        ProviderError::rate_limited(self.header("Retry-After").and_then(parse_retry_after))
    }
}

// TODO: support also the date format
fn parse_retry_after(value: &str) -> Option<i64> {
    value.trim().parse().ok()
}
