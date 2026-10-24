//! 控制协议 IPC：named pipe 服务端与客户端。

pub mod client;
pub mod dispatch;
pub mod server;

pub use client::{Client, ClientEvent};
