import React from 'react';
import { LinuxOutlined, WindowsOutlined } from '@ant-design/icons';

/**
 * The platform mark shown wherever a server is identified.
 *
 * Two things were inconsistent before this existed. The badge on cards, tables
 * and the detail hero paired the real Windows logo with a generic cloud-server
 * glyph for Linux, so the same slot meant "which OS" on one row and "it's a
 * server" on the next. And the OS <Select> in every form offered an *Apple*
 * logo next to the word Linux — a different operating system's trademark.
 * Both now use the platform's own mark.
 *
 * The badge's tint class comes from `platformClass` in utils/platform.
 */
export default function PlatformIcon({ serverType }: { serverType?: string }) {
  return serverType === 'windows' ? <WindowsOutlined /> : <LinuxOutlined />;
}
