use anyhow::Result;
use serde_json::Value;
use turbo_tasks::{primitives::StringVc, TryJoinIterExt};
use turbo_tasks_fs::FileSystemPathVc;
use turbopack::ecmascript::{
    chunk::{
        EcmascriptChunkItem, EcmascriptChunkItemContent, EcmascriptChunkItemContentVc,
        EcmascriptChunkItemVc, EcmascriptChunkPlaceable, EcmascriptChunkPlaceableVc,
        EcmascriptChunkVc, EcmascriptExports, EcmascriptExportsVc,
    },
    utils::stringify_js,
};
use turbopack_core::{
    asset::{Asset, AssetContentVc, AssetVc},
    chunk::{
        available_assets::AvailableAssetsVc, Chunk, ChunkGroupReferenceVc, ChunkGroupVc, ChunkItem,
        ChunkItemVc, ChunkVc, ChunkableAsset, ChunkableAssetVc, ChunkingContextVc,
    },
    ident::AssetIdentVc,
    reference::AssetReferencesVc,
};

#[turbo_tasks::function]
fn modifier() -> StringVc {
    StringVc::cell("chunks".to_string())
}

#[turbo_tasks::value(shared)]
pub struct WithChunksAsset {
    pub asset: EcmascriptChunkPlaceableVc,
    pub server_root: FileSystemPathVc,
    pub chunking_context: ChunkingContextVc,
}

#[turbo_tasks::value_impl]
impl Asset for WithChunksAsset {
    #[turbo_tasks::function]
    fn ident(&self) -> AssetIdentVc {
        self.asset.ident().with_modifier(modifier())
    }

    #[turbo_tasks::function]
    fn content(&self) -> AssetContentVc {
        unimplemented!()
    }

    #[turbo_tasks::function]
    async fn references(&self) -> Result<AssetReferencesVc> {
        Ok(AssetReferencesVc::cell(vec![ChunkGroupReferenceVc::new(
            ChunkGroupVc::from_asset(
                self.asset.into(),
                self.chunking_context,
                None,
                Some(self.asset.into()),
            ),
        )
        .into()]))
    }
}

#[turbo_tasks::value_impl]
impl ChunkableAsset for WithChunksAsset {
    #[turbo_tasks::function]
    fn as_chunk(
        self_vc: WithChunksAssetVc,
        context: ChunkingContextVc,
        available_assets: Option<AvailableAssetsVc>,
        current_availability_root: Option<AssetVc>,
    ) -> ChunkVc {
        EcmascriptChunkVc::new(
            context,
            self_vc.as_ecmascript_chunk_placeable(),
            available_assets,
            current_availability_root,
        )
        .into()
    }
}

#[turbo_tasks::value_impl]
impl EcmascriptChunkPlaceable for WithChunksAsset {
    #[turbo_tasks::function]
    async fn as_chunk_item(
        self_vc: WithChunksAssetVc,
        context: ChunkingContextVc,
    ) -> Result<EcmascriptChunkItemVc> {
        Ok(WithChunksChunkItem {
            context,
            inner: self_vc,
        }
        .cell()
        .into())
    }

    #[turbo_tasks::function]
    fn get_exports(&self) -> EcmascriptExportsVc {
        // TODO This should be EsmExports
        EcmascriptExports::Value.cell()
    }
}

#[turbo_tasks::value]
struct WithChunksChunkItem {
    context: ChunkingContextVc,
    inner: WithChunksAssetVc,
}

#[turbo_tasks::value_impl]
impl EcmascriptChunkItem for WithChunksChunkItem {
    #[turbo_tasks::function]
    fn chunking_context(&self) -> ChunkingContextVc {
        self.context
    }

    #[turbo_tasks::function]
    async fn content(&self) -> Result<EcmascriptChunkItemContentVc> {
        let inner = self.inner.await?;
        let group = ChunkGroupVc::from_asset(
            inner.asset.into(),
            inner.chunking_context,
            None,
            Some(inner.asset.into()),
        );
        let chunks = group.chunks().await?;
        let server_root = inner.server_root.await?;
        let mut client_chunks = Vec::new();
        for chunk_path in chunks.iter().map(|c| c.path()).try_join().await? {
            if let Some(path) = server_root.get_path_to(&chunk_path) {
                client_chunks.push(Value::String(path.to_string()));
            }
        }
        let module_id = stringify_js(
            &*inner
                .asset
                .as_chunk_item(inner.chunking_context)
                .id()
                .await?,
        );
        Ok(EcmascriptChunkItemContent {
            inner_code: format!(
                "__turbopack_esm__({{
  default: () => {},
  chunks: () => chunks
}});
const chunks = {};
",
                module_id,
                Value::Array(client_chunks)
            )
            .into(),
            ..Default::default()
        }
        .cell())
    }
}

#[turbo_tasks::value_impl]
impl ChunkItem for WithChunksChunkItem {
    #[turbo_tasks::function]
    fn asset_ident(&self) -> AssetIdentVc {
        self.inner.ident()
    }

    #[turbo_tasks::function]
    fn references(&self) -> AssetReferencesVc {
        self.inner.references()
    }
}
