import styled from "styled-components";

export default function StyledContainer({
  children,
  isPressable = false,
  onClick,
  className,
}: {
  children: React.ReactNode;
  isPressable?: boolean;
  className?: string;
  onClick?: () => void;
}) {
  return isPressable ? (
    <ButtonContainer className={className} onClick={onClick}>
      {children}
    </ButtonContainer>
  ) : (
    <DivContainer className={className}>{children}</DivContainer>
  );
}

const DivContainer = styled.div`
  background: linear-gradient(
    180deg,
    rgba(20, 26, 34, 0.5) 0%,
    rgba(16, 21, 16, 0.5) 100%
  );

  border: 1px solid rgba(249, 249, 249, 0.1);
  border-radius: 24px;
  backdrop-filter: blur(20px);
  box-shadow: 0px 4px 24px rgba(0, 0, 0, 0.25);
`;

const ButtonContainer = styled.button`
  width: 100%;
  background: linear-gradient(
    180deg,
    #141a22 0%,
    #141a22 11.79%,
    #131a22 21.38%,
    #131922 29.12%,
    #131922 35.34%,
    #131921 40.37%,
    #131921 44.56%,
    #121820 48.24%,
    #121820 51.76%,
    #12171f 55.44%,
    #11171f 59.63%,
    #11171e 64.66%,
    #11161e 70.88%,
    #11161d 78.62%,
    #10151c 88.21%,
    #10151c 100%
  );
  box-shadow:
    0px 4px 6px 0px rgba(0, 0, 0, 0.14),
    0px 0px 0px 1px #0c0d0f,
    0px 9px 14px -5px rgba(255, 255, 255, 0.1) inset;
  border: 1px solid rgba(249, 249, 249, 0.1);
  border-radius: 24px;
  backdrop-filter: blur(20px);
  box-shadow: 0px 4px 24px rgba(0, 0, 0, 0.25);
`;
