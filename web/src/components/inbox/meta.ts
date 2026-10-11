import type { Schemas } from "@/lib/api/client";

export type Lane = Schemas["Lane"];
export type Opportunity = Schemas["Opportunity"];

export const LANE_LABEL: Record<Lane, string> = { inbound: "Inbound", media: "Media & speaking", thought_leadership: "Thought leadership" };

export const TYPE_LABEL: Record<string, string> = {
  recommendation_request: "Asks for a recommendation",
  pain_point: "Pain point",
  journalist_query: "Journalist query",
  podcast_guest: "Podcast guest call",
  speaking_cfp: "Call for speakers",
  guest_post: "Guest post",
  rfp: "RFP",
  competitor_complaint: "Competitor complaint",
  trending_discussion: "Trending discussion",
};

export const FLAG_LABEL: Record<string, string> = {
  performance_claim_risk: "Reply may invite performance talk",
  avoided_audience: "Avoided audience",
  non_target_region: "Outside target region",
  hype_or_scam: "Hype or scam",
  excluded_venue: "We don't post here",
  competitor_mention: "Competitor mentioned",
};

export const FACTOR_LABEL: Record<string, string> = {
  relevance: "Relevance", intent: "Intent", fit: "Fit", recency: "Recency", reach: "Reach", deadline: "Deadline",
};

export const DISMISS_REASONS: { v: string; label: string }[] = [
  { v: "off_topic", label: "Off topic" },
  { v: "low_intent", label: "Low intent" },
  { v: "wrong_fit", label: "Wrong fit for us" },
  { v: "too_old", label: "Too old" },
  { v: "duplicate", label: "Duplicate" },
  { v: "bad_source", label: "Bad source" },
  { v: "other", label: "Other" },
];

export function age(iso: string): string {
  const mins = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 60000));
  if (mins < 60) return `${mins}m ago`;
  const h = Math.round(mins / 60);
  if (h < 48) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}

export function deadlineLabel(iso: string): { text: string; urgent: boolean } {
  const h = (new Date(iso).getTime() - Date.now()) / 3_600_000;
  if (h <= 0) return { text: "deadline passed", urgent: true };
  if (h < 48) return { text: `due in ${Math.round(h)}h`, urgent: true };
  return { text: `due in ${Math.round(h / 24)}d`, urgent: false };
}
