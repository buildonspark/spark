import styled, { css } from "styled-components";

interface ButtonsProps {
  direction?: "horizontal" | "vertical";
  kind?: "primary" | "secondary";
  icon?: React.ReactNode;
  text?: string;
}

export default function Button({
  direction = "horizontal",
  kind = "primary",
  icon,
  text,
}: ButtonsProps) {
  return (
    <StyledButton direction={direction} kind={kind}>
      {icon}
      {text}
    </StyledButton>
  );
}

const StyledButton = styled.button<{
  direction: "horizontal" | "vertical";
  kind: "primary" | "secondary";
}>`
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: center;

  padding: 14px 32px;
  gap: 12px;

  border-radius: 999px;
  font-family: "Decimal";

  font-size: 15px;
  line-height: 38px;

  text-align: center;
  margin-top: 10px;
  position: relative;

  border: 1px solid;

  border-image-source: linear-gradient(
    180deg,
    rgba(249, 249, 249, 0.12) -25%,
    rgba(249, 249, 249, 0.04) 117.5%
  );

  box-shadow: 0px 0px 0px 1px rgba(12, 13, 15, 0.7),
    0px 4px 20px 0px rgba(0, 0, 0, 0.5), 0px 1px 4px 0px rgba(0, 0, 0, 0.25),
    0px 8px 16px 0px rgba(255, 255, 255, 0.1) inset;

  ${({ kind }) =>
    kind === "primary"
      ? css`
          background: linear-gradient(
            180deg,
            #0e3154 0%,
            rgba(14, 49, 84, 0.5) 100%
          );
        `
      : css`
          background: rgba(14, 49, 84, 0.2);
        `}

  ${({ direction }) =>
    direction === "vertical" &&
    css`
      border-radius: 12px;
      flex-direction: column;
      gap: 0px;
      line-height: 24px;
    `}
  &:hover {
    background: linear-gradient(180deg, #0e3154 0%, rgba(14, 49, 84, 0.5) 100%);
  }
`;
