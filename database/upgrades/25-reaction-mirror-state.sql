-- v25 (compatible with v19+): reaction mirror summary state
CREATE TABLE reaction_mirror_state (
    dc_chan_id          TEXT NOT NULL,
    dc_chan_receiver    TEXT NOT NULL,
    dc_msg_id           TEXT NOT NULL,
    target_mxid         TEXT NOT NULL,
    mirrored_emoji      TEXT NOT NULL DEFAULT '',
    summary_thread_id   TEXT NOT NULL DEFAULT '',
    summary_message_id  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (dc_chan_id, dc_chan_receiver, dc_msg_id),
    CONSTRAINT reaction_mirror_portal_fkey FOREIGN KEY (dc_chan_id, dc_chan_receiver) REFERENCES portal (dcid, receiver) ON DELETE CASCADE
);
