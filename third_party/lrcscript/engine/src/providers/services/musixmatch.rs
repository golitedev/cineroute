//! Anonymous mobile API adapted from Myzel394/navidrome-musixmatch-plugin (MIT).
use crate::{
    config::{PluginConfig, ProviderParams},
    ext::TrackInfoExt,
    format::lrc,
    matching,
    providers::{LyricsProvider, ProviderResult, error::ProviderError, http::Http},
    track::TrackInfo,
    types::{Lyrics, LyricsKind},
};
use regex::Regex;
use serde::Deserialize;
use serde_json::Value;
use std::{
    sync::OnceLock,
    time::{SystemTime, UNIX_EPOCH},
};

const BASE_URL: &str = "https://apic-appmobile.musixmatch.com/ws/1.1";
const MOBILE_AGENT: &str = "Musixmatch/2025120901 CFNetwork/3860.300.31 Darwin/25.2.0";
const POISON_PREFIX: &str = "wob gopini den tefe woxica fero gogoh vudob wiya";

pub struct Musixmatch {
    base_url: String,
}
#[derive(Deserialize)]
struct Envelope {
    message: Message,
}
#[derive(Deserialize)]
struct Message {
    header: Header,
    #[serde(default)]
    body: Value,
}
#[derive(Deserialize)]
struct Header {
    status_code: i32,
}
#[derive(Deserialize)]
struct RichLine {
    ts: f64,
    x: String,
}

impl Musixmatch {
    pub fn create(params: &ProviderParams) -> Box<dyn LyricsProvider> {
        Box::new(Self {
            base_url: params
                .get("baseUrl")
                .unwrap_or(BASE_URL)
                .trim_end_matches('/')
                .into(),
        })
    }

    fn get(&self, action: &str, params: Vec<(&str, String)>) -> ProviderResult<Option<Value>> {
        let millis = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_millis();
        let mut request = Http::get(format!("{}/{action}", self.base_url))
            .header("User-Agent", MOBILE_AGENT)
            .header("X-User-Agent", MOBILE_AGENT)
            .header("x-mxm-app-version", "10.1.1")
            .header("Accept-Language", "en-US,en;q=0.9")
            .header("Accept", "application/json")
            .param("app_id", "mac-ios-v2.0")
            .param("t", millis.to_string());
        for (key, value) in params {
            request = request.param(key, value);
        }
        let response = request.send()?;
        if response.status != 200 {
            return match response.status {
                204 | 404 => Ok(None),
                _ => Err(response.unexpected_status("Musixmatch mobile API")),
            };
        }
        let envelope: Envelope = response.json("Musixmatch mobile API")?;
        match envelope.message.header.status_code {
            200 => Ok(Some(envelope.message.body)),
            204 | 404 => Ok(None),
            429 => Err(response.rate_limited()),
            status @ (401 | 403 | 500..=599) => Err(ProviderError::unavailable(
                format!("Musixmatch mobile API returned status {status}"),
                response.header("Retry-After").and_then(|s| s.parse().ok()),
            )),
            status => Err(ProviderError::other(format!(
                "Musixmatch mobile API returned status {status}"
            ))),
        }
    }
}

impl LyricsProvider for Musixmatch {
    fn supported_kinds(&self) -> &'static [LyricsKind] {
        &[LyricsKind::Lrc, LyricsKind::Plain]
    }

    fn fetch_lyrics(
        &self,
        track: &TrackInfo,
        cfg: &PluginConfig,
    ) -> ProviderResult<Option<Lyrics>> {
        if !track.has_artist() {
            return Err(ProviderError::other("track has no artist"));
        }
        // An anonymous API token is obtained per lookup and never retained or logged.
        let Some(body) = self.get("token.get", vec![("user_language", "en".into())])? else {
            return Ok(None);
        };
        let token = body
            .get("user_token")
            .and_then(Value::as_str)
            .filter(|s| !s.is_empty())
            .ok_or_else(|| ProviderError::other("Musixmatch returned no anonymous token"))?;
        let mut params = vec![
            ("format", "json".into()),
            ("namespace", "lyrics_richsynched".into()),
            ("optional_calls", "track.richsync".into()),
            ("subtitle_format", "lrc".into()),
            ("q_artist", track.artist.clone()),
            ("q_track", track.title.clone()),
            ("usertoken", token.into()),
            ("q_duration", track.duration.round().to_string()),
        ];
        if !track.album.is_empty() {
            params.push(("q_album", track.album.clone()));
        }
        let Some(body) = self.get("macro.subtitles.get", params)? else {
            return Ok(None);
        };
        let calls = body
            .get("macro_calls")
            .and_then(Value::as_object)
            .ok_or_else(|| ProviderError::other("Musixmatch response has no macro calls"))?;
        if let Some(call) = calls.get("matcher.track.get") {
            if let Some(error) = blocked_call(call) {
                return Err(error);
            }
        }
        let Some(matched) = calls
            .get("matcher.track.get")
            .and_then(call_body)
            .and_then(|b| b.get("track"))
        else {
            return Ok(None);
        };
        validate_match(track, cfg, matched)?;
        if let Some(lyrics) = pick_lyrics(calls, &cfg.lyrics_type_priority) {
            return Ok(Some(lyrics));
        }
        // A restricted richsync response must not prevent an available subtitle/plain fallback.
        for name in [
            "track.richsync.get",
            "track.subtitles.get",
            "track.lyrics.get",
        ] {
            if let Some(call) = calls.get(name) {
                if let Some(error) = blocked_call(call) {
                    return Err(error);
                }
            }
        }
        Ok(None)
    }
}

fn call_body(call: &Value) -> Option<&Value> {
    (call.pointer("/message/header/status_code")?.as_i64()? == 200)
        .then(|| call.pointer("/message/body"))
        .flatten()
}

fn blocked_call(call: &Value) -> Option<ProviderError> {
    match call.pointer("/message/header/status_code")?.as_i64()? {
        429 => Some(ProviderError::rate_limited(None)),
        status @ (401 | 403 | 500..=599) => Some(ProviderError::unavailable(
            format!("Musixmatch lookup returned status {status}"),
            None,
        )),
        _ => None,
    }
}

fn validate_match(track: &TrackInfo, cfg: &PluginConfig, matched: &Value) -> ProviderResult<()> {
    let title = matched
        .get("track_name")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let artist = matched
        .get("artist_name")
        .and_then(Value::as_str)
        .unwrap_or_default();
    if !matching::titles_match(&track.title, title) || !matching::artists_match(track, artist) {
        return Err(ProviderError::other(
            "Musixmatch artist/title/version mismatch",
        ));
    }
    if let Some(duration) = matched.get("track_length").and_then(Value::as_f64) {
        if !duration.is_finite()
            || duration <= 0.0
            || (duration - f64::from(track.duration)).abs() > cfg.duration_tolerance.as_secs_f64()
        {
            return Err(ProviderError::other("Musixmatch duration mismatch"));
        }
    }
    if cfg.require_album_match
        && !track.matches_album(
            matched
                .get("album_name")
                .and_then(Value::as_str)
                .unwrap_or_default(),
        )
    {
        return Err(ProviderError::other("Musixmatch album mismatch"));
    }
    Ok(())
}

fn allowed(text: &str) -> bool {
    static TAG: OnceLock<Regex> = OnceLock::new();
    let stripped = TAG
        .get_or_init(|| Regex::new(r"\[\d{1,3}:\d{2}(?:[.:]\d{1,3})?\]").unwrap())
        .replace_all(text, "");
    !text.trim().is_empty()
        && !stripped
            .to_lowercase()
            .split_whitespace()
            .collect::<Vec<_>>()
            .join(" ")
            .starts_with(POISON_PREFIX)
}

fn richsync(body: &Value) -> Option<String> {
    let raw = body.pointer("/richsync/richsync_body")?.as_str()?;
    let lines: Vec<RichLine> = serde_json::from_str(raw).ok()?;
    let text = lines
        .into_iter()
        .filter(|line| line.ts.is_finite() && line.ts >= 0.0 && !line.x.trim().is_empty())
        .map(|line| {
            format!(
                "[{}]{}",
                lrc::format_timestamp((line.ts * 1000.0).round() as i64),
                line.x
            )
        })
        .collect::<Vec<_>>()
        .join("\n");
    allowed(&text).then_some(text)
}

fn pick_lyrics(calls: &serde_json::Map<String, Value>, order: &[LyricsKind]) -> Option<Lyrics> {
    for kind in order {
        match kind {
            LyricsKind::Lrc => {
                if let Some(text) = calls
                    .get("track.richsync.get")
                    .and_then(call_body)
                    .and_then(richsync)
                {
                    return Some(Lyrics::Lrc(text));
                }
                if let Some(items) = calls
                    .get("track.subtitles.get")
                    .and_then(call_body)
                    .and_then(|b| b.get("subtitle_list"))
                    .and_then(Value::as_array)
                {
                    for item in items {
                        if let Some(text) = item
                            .pointer("/subtitle/subtitle_body")
                            .and_then(Value::as_str)
                        {
                            if lrc::is_synced(text) && allowed(text) {
                                return Some(Lyrics::Lrc(text.into()));
                            }
                        }
                    }
                }
            }
            LyricsKind::Plain => {
                if let Some(text) = calls
                    .get("track.lyrics.get")
                    .and_then(call_body)
                    .and_then(|b| b.pointer("/lyrics/lyrics_body"))
                    .and_then(Value::as_str)
                {
                    if allowed(text) {
                        return Some(Lyrics::Plain(text.into()));
                    }
                }
            }
            _ => {}
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    fn call(body: Value) -> Value {
        json!({"message": {"header": {"status_code": 200}, "body": body}})
    }
    fn track() -> TrackInfo {
        TrackInfo {
            title: "Song (Album Version)".into(),
            artist: "Beyoncé".into(),
            artists: vec![crate::track::Artist {
                name: "Beyoncé".into(),
            }],
            album: "Album".into(),
            duration: 180.0,
            ..Default::default()
        }
    }
    #[test]
    fn normalization_handles_accents_editions_and_artist_features() {
        assert!(matching::artists_match(&track(), "Beyonce feat. Guest"));
        assert!(matching::titles_match("Song (Album Version)", "Song"));
        assert!(!matching::titles_match("Song", "Different Song"));
        assert!(!matching::titles_match("Song (A Remix)", "Song (B Remix)"));
    }
    #[test]
    fn validates_identity_duration_album_and_versions_without_global_cooldown() {
        let valid = json!({"track_name":"Song", "artist_name":"Beyonce", "track_length":180,"album_name":"Album"});
        assert!(validate_match(&track(), &PluginConfig::default(), &valid).is_ok());
        for (key, value) in [
            ("track_name", json!("Song (Live)")),
            ("artist_name", json!("Someone else")),
            ("track_length", json!(200)),
            ("track_name", json!("")),
        ] {
            let mut wrong = valid.clone();
            wrong[key] = value;
            assert_eq!(
                validate_match(&track(), &PluginConfig::default(), &wrong)
                    .unwrap_err()
                    .cooldown_seconds(),
                None
            );
        }
        let cfg = PluginConfig {
            require_album_match: true,
            ..Default::default()
        };
        let mut wrong = valid;
        wrong["album_name"] = json!("Unrelated Album");
        assert!(validate_match(&track(), &cfg, &wrong).is_err());
    }
    #[test]
    fn richsync_becomes_lrc_and_malformed_richsync_falls_back() {
        let mut calls = serde_json::Map::new();
        calls.insert(
            "track.richsync.get".into(),
            call(json!({"richsync":{"richsync_body":r#"[{"ts":1.25,"x":"Words"}]"#}})),
        );
        let order = &[LyricsKind::Lrc, LyricsKind::Plain];
        assert_eq!(
            pick_lyrics(&calls, order),
            Some(Lyrics::Lrc("[00:01.25]Words".into()))
        );
        calls.insert(
            "track.richsync.get".into(),
            call(json!({"richsync":{"richsync_body":"invalid"}})),
        );
        calls.insert(
            "track.subtitles.get".into(),
            call(json!({"subtitle_list":[{"subtitle":{"subtitle_body":"[00:02.00]Other words"}}]})),
        );
        assert_eq!(
            pick_lyrics(&calls, order),
            Some(Lyrics::Lrc("[00:02.00]Other words".into()))
        );
    }
    #[test]
    fn poisoned_and_blank_lyrics_are_rejected_but_valid_fallback_survives() {
        assert!(!allowed(&format!("[00:01.00]{POISON_PREFIX}")));
        assert!(!allowed("   "));
        let mut calls = serde_json::Map::new();
        calls.insert("track.subtitles.get".into(),call(json!({"subtitle_list":[{"subtitle":{"subtitle_body":format!("[00:01.00]{POISON_PREFIX}")}}]})));
        calls.insert(
            "track.lyrics.get".into(),
            call(json!({"lyrics":{"lyrics_body":"Valid words"}})),
        );
        assert_eq!(
            pick_lyrics(&calls, &[LyricsKind::Lrc, LyricsKind::Plain]),
            Some(Lyrics::Plain("Valid words".into()))
        );
        assert_eq!(pick_lyrics(&calls, &[LyricsKind::Lrc]), None);
    }
    #[test]
    fn api_blocks_are_temporary_while_not_found_is_not() {
        assert_eq!(
            blocked_call(&json!({"message":{"header":{"status_code":429}}}))
                .unwrap()
                .cooldown_seconds(),
            Some(60)
        );
        assert!(
            blocked_call(&json!({"message":{"header":{"status_code":401}}}))
                .unwrap()
                .cooldown_seconds()
                .is_some()
        );
        assert!(blocked_call(&json!({"message":{"header":{"status_code":404}}})).is_none());
    }
}
