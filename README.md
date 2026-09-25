# tripleS Cosmo Tool — v4 + SPIN

Localhost UI + native Go backend for tripleS COSMO.

The transfer feature is the working v4 flow. SPIN was added from one captured normal official iOS flow:

- `GET /bff/v3/spin/tickets/tripleS`
- `GET /bff/v3/spin/price`
- `GET /bff/v3/point?artistId=tripleS`
- `GET /bff/v4/seasons/tripleS`
- `POST /bff/v3/spin/pre-sign` with the used token id
- normal on-chain Objekt transfer to the exact `cosmo-spin` account resolved through COSMO user search
- encrypted `POST /bff/v3/spin`
- user manually chooses one of 16 slots
- encrypted `POST /bff/v3/spin/complete`
- server-returned 16-slot result is rendered without local result generation

The project does not reimplement COSMO's request encryption. At first build it vendors the already-required `cosmo-tui` module and generates a narrow exported wrapper around that package's existing AES request-body helper. No crypto key is written by this project.

Safety choices: ticket-only SPIN, no automatic repeat, no automatic choice, no reroll, no speculative result requests, no automatic retry of consequential SPIN requests.

## FIX5 변경사항
- 새로고침 시 실행 중인 로컬 로그인 세션 자동 복원
- SPIN 16칸 직접 선택 UI 복구
- 칸 선택 즉시 결과 요청 1회 전송, 성공 시 16칸 전체 공개
- 내가 선택한 칸과 실제 획득 Objekt를 동시에 표시
- 고정 3.5초/8초 대기 제거
- SPIN 화면 UI 정리
# cosmospin-pc
