export interface SseEvent {
  event: string;
  data: string;
}

/**
 * Incremental parser for a text/event-stream body. Feed it chunks as they
 * arrive; it returns the events completed so far and keeps any partial event
 * for the next chunk.
 */
export class SseParser {
  private buffer = "";

  push(chunk: string): SseEvent[] {
    this.buffer += chunk.replace(/\r\n/g, "\n");
    const events: SseEvent[] = [];
    let end: number;
    while ((end = this.buffer.indexOf("\n\n")) !== -1) {
      const block = this.buffer.slice(0, end);
      this.buffer = this.buffer.slice(end + 2);
      let event = "message";
      const data: string[] = [];
      for (const line of block.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
      }
      if (data.length) events.push({ event, data: data.join("\n") });
    }
    return events;
  }
}
