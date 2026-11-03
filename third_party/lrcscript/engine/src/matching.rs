//! Conservative identity checks: discard edition labels, retain recording versions.
use crate::{config::PluginConfig, ext::TrackInfoExt, track::TrackInfo};
use regex::{Captures, Regex};
use std::sync::OnceLock;
use unicode_normalization::{UnicodeNormalization, char::is_combining_mark};

fn key(value: &str) -> String {
    value
        .nfd()
        .filter(|c| !is_combining_mark(*c))
        .flat_map(char::to_lowercase)
        .filter(|c| c.is_alphanumeric())
        .collect()
}
fn harmless(value: &str) -> bool {
    let words: Vec<_> = value
        .split(|c: char| !c.is_alphanumeric())
        .filter(|s| !s.is_empty())
        .collect();
    !words.is_empty()
        && words.iter().all(|word| {
            word.chars().all(|c| c.is_ascii_digit())
                || matches!(
                    word.to_lowercase().as_str(),
                    "album"
                        | "single"
                        | "original"
                        | "version"
                        | "ver"
                        | "remaster"
                        | "remastered"
                        | "deluxe"
                        | "edition"
                        | "anniversary"
                        | "expanded"
                        | "bonus"
                        | "track"
                        | "tracks"
                        | "special"
                        | "stereo"
                        | "mono"
                        | "explicit"
                        | "clean"
                        | "digital"
                )
        })
}
fn title_key(value: &str) -> String {
    static BRACKETS: OnceLock<Regex> = OnceLock::new();
    static DASH: OnceLock<Regex> = OnceLock::new();
    let brackets =
        BRACKETS.get_or_init(|| Regex::new(r"\(([^()]*)\)|\[([^\[\]]*)\]|\{([^{}]*)\}").unwrap());
    let mut text = value.to_string();
    // Repeated passes handle nested edition brackets without stripping unknown text.
    for _ in 0..4 {
        let next = brackets
            .replace_all(&text, |cap: &Captures| {
                let inside = cap.iter().skip(1).flatten().next().unwrap().as_str();
                let lower = inside.trim().to_lowercase();
                let version = inside.split(|c: char| !c.is_alphanumeric()).any(|word| {
                    matches!(
                        word.to_lowercase().as_str(),
                        "live"
                            | "remix"
                            | "acoustic"
                            | "instrumental"
                            | "karaoke"
                            | "demo"
                            | "edit"
                            | "cover"
                    )
                });
                if harmless(inside)
                    || (!version
                        && ["feat.", "feat ", "ft.", "ft ", "featuring "]
                            .iter()
                            .any(|prefix| lower.starts_with(prefix)))
                {
                    " ".to_string()
                } else {
                    format!(" {inside} ")
                }
            })
            .into_owned();
        if next == text {
            break;
        }
        text = next;
    }
    let dash = DASH.get_or_init(|| Regex::new(r"\s+[-–—]\s+").unwrap());
    let parts: Vec<_> = dash.split(&text).collect();
    key(&parts
        .into_iter()
        .enumerate()
        .filter(|(index, part)| *index == 0 || !harmless(part))
        .map(|(_, part)| part)
        .collect::<Vec<_>>()
        .join(" "))
}
pub fn titles_match(wanted: &str, actual: &str) -> bool {
    let wanted = title_key(wanted);
    !wanted.is_empty() && wanted == title_key(actual)
}
pub fn artists_match(track: &TrackInfo, actual: &str) -> bool {
    static FEATURE: OnceLock<Regex> = OnceLock::new();
    let feature =
        FEATURE.get_or_init(|| Regex::new(r"(?i)\s+(?:feat\.?|ft\.?|featuring)\s+").unwrap());
    let normalize = |value: &str| key(feature.split(value).next().unwrap_or(value));
    let actual = normalize(actual);
    !actual.is_empty()
        && (actual == normalize(&track.artist)
            || actual == normalize(track.first_artist().unwrap_or(&track.artist)))
}
pub fn duration_matches(track: &TrackInfo, cfg: &PluginConfig, duration: Option<f64>) -> bool {
    duration.is_some_and(|duration| {
        duration.is_finite()
            && duration > 0.0
            && (duration - f64::from(track.duration)).abs() <= cfg.duration_tolerance.as_secs_f64()
    })
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn editions_and_accents_match_but_versions_and_subtitles_do_not() {
        for title in ["Original", "Clean", "Version", "Live"] {
            assert!(titles_match(title, title));
        }
        assert!(titles_match("Kryptonite (Album Version)", "Kryptonite"));
        assert!(titles_match("Song - Remastered 2011", "Song"));
        assert!(titles_match("L’été (feat. Guest)", "L'ete"));
        for other in [
            "Song (Live)",
            "Song (Remix)",
            "Song (Acoustic)",
            "Song - Radio Edit",
            "Song (Piano)",
            "Song 2",
            "Song (Live at Sydney)",
        ] {
            assert!(!titles_match("Song", other), "{other}");
        }
        assert!(!titles_match("Song (A Remix)", "Song (B Remix)"));
        assert!(!titles_match(
            "Cuando Volveras",
            "Cuando Volveras (English Version)"
        ));
        assert!(titles_match(
            "Cuando Volveras (English Version) [Bonus Track]",
            "Cuando Volveras (English Version)"
        ));
        assert!(titles_match("Song (Live)", "Song - Live"));
    }
    #[test]
    fn artists_are_verified_and_invalid_durations_cannot_panic() {
        let track = TrackInfo {
            artist: "Beyoncé".into(),
            duration: 180.0,
            ..Default::default()
        };
        assert!(artists_match(&track, "Beyonce feat. Guest"));
        assert!(!artists_match(&track, "Beyonce Cover Band"));
        for bad in [-1.0, 0.0, f64::NAN, f64::INFINITY, 200.0] {
            assert!(!duration_matches(
                &track,
                &PluginConfig::default(),
                Some(bad)
            ));
        }
    }
}
