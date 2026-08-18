import moment from "moment-timezone/builds/moment-timezone-with-data-10-year-range.js";

/** browserTimeZone is the IANA zone the browser reports, e.g. "Asia/Shanghai". */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** isValidTimeZone screens a name before it is sent to the server. */
export function isValidTimeZone(zone: string): boolean {
  return moment.tz.zone(zone) !== null;
}

/** Names come from the bundled IANA database, not the browser's Intl object. */
export function timeZoneNames(): string[] {
  return moment.tz.names();
}

/**
 * timeZoneOffsetLabel renders a zone's current offset, e.g. "UTC+8" or
 * "UTC+5:30". The bundled timezone database keeps this independent of browser
 * extensions that spoof Intl or Date#getTimezoneOffset.
 */
export function timeZoneOffsetLabel(zone: string, at: Date | number = Date.now()): string {
  const info = moment.tz.zone(zone);
  if (!info) return "";

  // Moment follows Date#getTimezoneOffset and reports minutes west of UTC;
  // labels in the UI use the more familiar positive-east convention.
  const minutes = -info.utcOffset(typeof at === "number" ? at : at.getTime());

  const sign = minutes < 0 ? "-" : "+";
  const abs = Math.abs(minutes);
  const hours = Math.floor(abs / 60);
  const rest = abs % 60;
  // Not every zone is a whole hour: Kolkata is +5:30, Kathmandu +5:45.
  return rest === 0
    ? `UTC${sign}${hours}`
    : `UTC${sign}${hours}:${String(rest).padStart(2, "0")}`;
}

export interface ZonedDateParts {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
  second: number;
}

/** Convert an instant using bundled IANA rules and timezone-neutral UTC getters. */
export function zonedDateParts(at: Date, zone: string): ZonedDateParts | null {
  const info = moment.tz.zone(zone);
  if (!info || Number.isNaN(at.getTime())) return null;

  const wall = new Date(at.getTime() - info.utcOffset(at.getTime()) * 60_000);
  return {
    year: wall.getUTCFullYear(),
    month: wall.getUTCMonth() + 1,
    day: wall.getUTCDate(),
    hour: wall.getUTCHours(),
    minute: wall.getUTCMinutes(),
    second: wall.getUTCSeconds(),
  };
}
